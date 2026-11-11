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

// TestDriverDay is Phase 5 end to end: a driver's working day with a real
// (simulated) truck. The ELD's readings write driving and the stops; the
// driver adds the pre-trip they forgot, goes off duty, reads the day and
// signs it. Nobody writes "driving" by hand.
func TestDriverDay(t *testing.T) {
	tn := newTenant(t)
	ali, aliC := tn.newDriver(t)
	truck, eld := tn.newTruck(t), tn.newELD(t)
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	aliC.do(t, http.MethodPut, "/api/v1/drivers/me/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)

	// Fixture, not API: the ELD went in and Ali took the truck yesterday,
	// so the replayed trip (it ends now, so it began a while ago) is on them.
	_, err := testDB.Exec(t.Context(),
		`UPDATE eld_truck_assignments SET started_at = now() - interval '1 day' WHERE eld_device_id = $1`, eld.ID)
	require.NoError(t, err)
	_, err = testDB.Exec(t.Context(),
		`UPDATE driver_truck_assignments SET started_at = now() - interval '1 day' WHERE driver_id = $1`, ali.ID)
	require.NoError(t, err)

	// The day: pre-trip, drive, fuel stop (engine off: the device goes
	// quiet), drive on, unload (engine running), engine off.
	route, err := eldsim.BuiltInRoute("dallas-loop")
	require.NoError(t, err)
	plan := []eldsim.Phase{
		{Kind: eldsim.Idle, Duration: 3 * time.Minute, Note: "pre-trip"},
		{Kind: eldsim.Drive, ToMile: route.Length() * 0.4},
		{Kind: eldsim.Park, Duration: 20 * time.Minute, Note: "fuel, engine off"},
		{Kind: eldsim.Idle, Duration: 2 * time.Minute},
		{Kind: eldsim.Drive, ToMile: route.Length()},
		{Kind: eldsim.Idle, Duration: 10 * time.Minute, Note: "unloading"},
		{Kind: eldsim.Park},
	}
	mem := &eldsim.Memory{OdometerMi: 120000, EngineHours: 8000}
	trip, err := eldsim.Run(context.Background(), eldsim.Config{
		Client: &eldsim.Client{BaseURL: baseURL, Key: issued.DeviceKey},
		Truck:  eldsim.NewTruck(route, plan, mem.OdometerMi, mem.EngineHours), Memory: mem,
		Clock: eldsim.RealClock{}, Speedup: 3900, Sample: 10 * time.Second, SendEvery: time.Minute,
		Heartbeat: time.Minute, NotBefore: time.Now().Add(-23 * time.Hour),
	})
	require.NoError(t, err)
	require.Zero(t, trip.Rejected)

	// The two stops, as the readings tell them: the first standing reading
	// after each drive.
	var stops []time.Time
	rows, err := testDB.Query(t.Context(), `
		SELECT recorded_at FROM (
		    SELECT recorded_at, moving, lag(moving) OVER (ORDER BY recorded_at) AS was_moving
		    FROM eld_events WHERE eld_device_id = $1) e
		WHERE was_moving AND NOT moving ORDER BY recorded_at`, eld.ID)
	require.NoError(t, err)
	for rows.Next() {
		var at time.Time
		require.NoError(t, rows.Scan(&at))
		stops = append(stops, at)
	}
	require.NoError(t, rows.Err())
	require.Len(t, stops, 2, "fuel stop and customer")

	t.Run("the driver adds the pre-trip they forgot, then goes off duty", func(t *testing.T) {
		aliC.do(t, http.MethodPost, "/api/v1/drivers/me/duty-status/records", map[string]any{
			"status": "on_duty", "started_at": trip.FirstRecorded.Add(-15 * time.Minute).Format(time.RFC3339),
			"reason": "pre-trip: forgot to press on duty",
		}, http.StatusCreated, nil)
		aliC.do(t, http.MethodPost, "/api/v1/drivers/me/duty-status", map[string]any{"status": "off_duty"}, http.StatusCreated, nil)
	})

	// The day may cross Chicago's midnight: read every day the trip touched.
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	var logs []dailyLogResp
	for day := trip.FirstRecorded.Add(-15 * time.Minute).In(chicago); ; day = day.AddDate(0, 0, 1) {
		var l dailyLogResp
		aliC.do(t, http.MethodGet, "/api/v1/drivers/me/logs/"+day.Format(time.DateOnly), nil, http.StatusOK, &l)
		logs = append(logs, l)
		if day.Format(time.DateOnly) == time.Now().In(chicago).Format(time.DateOnly) {
			break
		}
	}

	t.Run("the logbook tells the day", func(t *testing.T) {
		// One line per record, in order (a record cut at midnight is one).
		var timeline []string
		var starts []string
		lastID := ""
		var driving int64
		for _, l := range logs {
			for _, sg := range l.Segments {
				if sg.Record.ID != lastID {
					timeline = append(timeline, sg.Record.Status+"/"+sg.Record.Origin)
					starts = append(starts, sg.Record.StartedAt)
					lastID = sg.Record.ID
				}
			}
			driving += l.Totals.Driving
		}
		assert.Equal(t, []string{
			"on_duty/driver", // the pre-trip, added
			"driving/auto",   // the ELD: the truck moved
			"on_duty/auto",   // standing 5 minutes at the fuel stop
			"driving/auto",   // moving again
			"on_duty/auto",   // standing 5 minutes at the customer
			"off_duty/driver",
		}, timeline)

		require.Len(t, starts, 6)
		mustTime := func(s string) time.Time {
			at, err := time.Parse(time.RFC3339, s)
			require.NoError(t, err)
			return at
		}
		assert.True(t, mustTime(starts[2]).Equal(stops[0].Add(5*time.Minute).Truncate(time.Second)),
			"on duty 5 minutes after the fuel stop: %s vs %s", starts[2], stops[0])
		assert.True(t, mustTime(starts[4]).Equal(stops[1].Add(5*time.Minute).Truncate(time.Second)),
			"on duty 5 minutes after arriving")
		assert.Positive(t, driving)
		assert.Less(t, driving, int64(trip.LastRecorded.Sub(trip.FirstRecorded)/time.Second), "not the whole trip")
	})

	t.Run("the office sees the same, and Ali signs", func(t *testing.T) {
		for _, l := range logs {
			var office dailyLogResp
			tn.Admin.do(t, http.MethodGet, "/api/v1/drivers/"+ali.ID+"/logs/"+l.Date, nil, http.StatusOK, &office)
			assert.Equal(t, l.Totals, office.Totals, l.Date)

			signed := certifyDay(t, aliC, l.Date)
			assert.True(t, signed.Certification.Certified, l.Date)
		}
	})
}

func certifyDay(t *testing.T, c client, date string) dailyLogResp {
	t.Helper()
	var l dailyLogResp
	c.do(t, http.MethodPost, "/api/v1/drivers/me/logs/"+date+"/certification", nil, http.StatusCreated, &l)
	return l
}
