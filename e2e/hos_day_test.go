package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/pkg/eldsim"
)

// TestHOSFromTheTruck is Phases 4-6 end to end: hours of service worked out
// from what a (simulated) truck reported, not from records typed in.
//
// Ali had already driven 5 hours this shift when he set off from Dallas to
// Houston (about 4 hours of driving, a 20-minute fuel stop, no 30-minute
// break). Past 8 hours of driving he needed a break and did not take it:
// the engine must find that, at the right moment, and the clocks, the
// daily logs and the fleet board must all tell the same story.
func TestHOSFromTheTruck(t *testing.T) {
	tn := newTenant(t)
	ali, aliC := tn.newDriver(t)
	truck, eld := tn.newTruck(t), tn.newELD(t)
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	aliC.do(t, http.MethodPut, "/api/v1/drivers/me/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)
	_, err := testDB.Exec(t.Context(),
		`UPDATE eld_truck_assignments SET started_at = now() - interval '1 day' WHERE eld_device_id = $1`, eld.ID)
	require.NoError(t, err)
	_, err = testDB.Exec(t.Context(),
		`UPDATE driver_truck_assignments SET started_at = now() - interval '1 day' WHERE driver_id = $1`, ali.ID)
	require.NoError(t, err)

	route, err := eldsim.BuiltInRoute("dallas-houston")
	require.NoError(t, err)
	plan := []eldsim.Phase{
		{Kind: eldsim.Idle, Duration: 3 * time.Minute},
		{Kind: eldsim.Drive, ToMile: route.Length() / 2},
		{Kind: eldsim.Park, Duration: 20 * time.Minute, Note: "fuel"},
		{Kind: eldsim.Idle, Duration: 2 * time.Minute},
		{Kind: eldsim.Drive, ToMile: route.Length()},
		{Kind: eldsim.Idle, Duration: 10 * time.Minute, Note: "unloading"},
		{Kind: eldsim.Park},
	}
	// The trip ends now, so it starts about its length ago (eldsim.Run).
	tripStart := time.Now().Add(-eldsim.TripDuration(route, plan)).Truncate(time.Second)

	// Fixture, not API: the 5 hours Ali drove before, then 10 minutes on
	// duty (a quick stop: no break) until the trip.
	shiftStart := tripStart.Add(-5*time.Hour - 10*time.Minute)
	for _, e := range []struct {
		status string
		at     time.Time
	}{
		{"driving", shiftStart},
		{"on_duty", tripStart.Add(-10 * time.Minute)},
	} {
		_, err := testDB.Exec(t.Context(), `
			INSERT INTO duty_status_events (company_id, driver_id, status, started_at, origin)
			VALUES ($1, $2, $3, $4, 'auto')`, tn.ID, ali.ID, e.status, e.at)
		require.NoError(t, err)
	}

	mem := &eldsim.Memory{OdometerMi: 200000, EngineHours: 9000}
	trip, err := eldsim.Run(context.Background(), eldsim.Config{
		Client: &eldsim.Client{BaseURL: baseURL, Key: issued.DeviceKey},
		Truck:  eldsim.NewTruck(route, plan, mem.OdometerMi, mem.EngineHours), Memory: mem,
		Clock: eldsim.RealClock{}, Speedup: 3900, Sample: 10 * time.Second, SendEvery: time.Minute,
		Heartbeat: time.Minute, NotBefore: time.Now().Add(-23 * time.Hour),
	})
	require.NoError(t, err)
	require.Zero(t, trip.Rejected)

	// The expected moment, worked out the simple way from the stored
	// records: where driving since the shift began reaches 8 hours.
	var breakDue time.Time
	{
		rows, err := testDB.Query(t.Context(), `
			SELECT status, started_at, lead(started_at, 1, now()) OVER (ORDER BY started_at)
			FROM duty_status_events WHERE driver_id = $1 AND record_status = 'active' ORDER BY started_at`, ali.ID)
		require.NoError(t, err)
		var driven time.Duration
		for rows.Next() {
			var status string
			var from, to time.Time
			require.NoError(t, rows.Scan(&status, &from, &to))
			if status != "driving" || !breakDue.IsZero() {
				continue
			}
			if driven+to.Sub(from) >= 8*time.Hour {
				breakDue = from.Add(8*time.Hour - driven)
			}
			driven += to.Sub(from)
		}
		require.NoError(t, rows.Err())
		require.False(t, breakDue.IsZero(), "the trip took him past 8 hours of driving")
	}

	var clocks hosResp
	aliC.do(t, http.MethodGet, "/api/v1/drivers/me/hos", nil, http.StatusOK, &clocks)

	t.Run("the missed break is found, from the truck's own readings", func(t *testing.T) {
		require.Len(t, clocks.Violations, 1, "%+v", clocks.Violations)
		v := clocks.Violations[0]
		assert.Equal(t, "break_30m", v.Rule)
		started, err := time.Parse(time.RFC3339, v.StartedAt)
		require.NoError(t, err)
		assert.True(t, started.Equal(breakDue), "at 8 hours of driving: %s, want %s", started, breakDue)

		assert.True(t, clocks.Shift.InShift)
		require.NotNil(t, clocks.Shift.StartedAt)
		assert.Equal(t, shiftStart.In(time.UTC), mustParse(t, *clocks.Shift.StartedAt).In(time.UTC))
		assert.Greater(t, clocks.Driving.UsedSeconds, 8*hour)
		assert.Equal(t, "on_duty", lastStatus(t, ali.ID), "unloading: standing 5 minutes made him on duty")
	})

	t.Run("the daily logs add up to the same driving", func(t *testing.T) {
		chicago, err := time.LoadLocation("America/Chicago")
		require.NoError(t, err)
		var driving int64
		var violations []violationResp
		for day := shiftStart.In(chicago); ; day = day.AddDate(0, 0, 1) {
			var l dailyLogResp
			var lv struct {
				Violations []violationResp `json:"violations"`
			}
			r := aliC.send(t, http.MethodGet, "/api/v1/drivers/me/logs/"+day.Format(time.DateOnly), nil)
			r.decode(t, &l)
			r.decode(t, &lv)
			driving += l.Totals.Driving
			violations = append(violations, lv.Violations...)
			if day.Format(time.DateOnly) == time.Now().In(chicago).Format(time.DateOnly) {
				break
			}
		}
		assert.Equal(t, clocks.Driving.UsedSeconds, driving, "the engine and the grid agree")
		require.NotEmpty(t, violations)
		assert.Equal(t, "break_30m", violations[0].Rule)
		var total int64
		for _, v := range violations {
			total += v.Seconds
		}
		assert.Equal(t, clocks.Violations[0].Seconds, total, "the same violation, cut at midnight if need be")
	})

	t.Run("the fleet board says the same", func(t *testing.T) {
		var b fleetResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/hos", nil, http.StatusOK, &b)
		require.Len(t, b.Items, 1)
		row := b.Items[0]
		assert.Equal(t, ali.ID, row.DriverID)
		assert.Equal(t, 1, row.Violations)
		assert.Equal(t, clocks.Driving.LeftSeconds, row.DrivingLeft)
		assert.InDelta(t, clocks.CanDriveSeconds, row.CanDriveSeconds, 10)
		require.NotNil(t, row.CurrentStatus)
		assert.Equal(t, "on_duty", *row.CurrentStatus)
	})
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return at
}

func lastStatus(t *testing.T, driverID string) string {
	t.Helper()
	var status string
	require.NoError(t, testDB.QueryRow(t.Context(), `
		SELECT status FROM duty_status_events WHERE driver_id = $1 AND record_status = 'active'
		ORDER BY started_at DESC LIMIT 1`, driverID).Scan(&status))
	return status
}
