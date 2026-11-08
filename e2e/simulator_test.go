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

// TestSimulatedTrip runs the real simulator (pkg/eldsim) against the
// running API: a truck drives a loop around Dallas with a fuel stop (engine
// off, heartbeats) and five minutes without signal, at 3900x (about a
// second of real time). Then the office's view and the stored history must
// match the trip exactly. A second, short trip checks that the device's
// memory carries over (seq, odometer, time).
func TestSimulatedTrip(t *testing.T) {
	tn := newTenant(t)
	truck := tn.newTruck(t)
	eld := tn.newELD(t)
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)

	// Fixture, not API: "the device was installed yesterday". Installing is
	// always "now" through the API (rightly), and a replayed trip may not
	// reach back before the installation (see eldsim.FitSpeedup).
	_, err := testDB.Exec(t.Context(),
		`UPDATE eld_truck_assignments SET started_at = now() - interval '1 day' WHERE eld_device_id = $1 AND ended_at IS NULL`, eld.ID)
	require.NoError(t, err)

	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)
	client := &eldsim.Client{BaseURL: baseURL, Key: issued.DeviceKey}
	me, err := client.Me(t.Context())
	require.NoError(t, err)
	require.NotNil(t, me.InstalledAt)

	route, err := eldsim.BuiltInRoute("dallas-loop")
	require.NoError(t, err)
	mem := &eldsim.Memory{OdometerMi: 120000, EngineHours: 8000}
	run := func(plan []eldsim.Phase, speedup float64, sample time.Duration, offline []eldsim.Window, notBefore time.Time) *eldsim.Stats {
		t.Helper()
		stats, err := eldsim.Run(context.Background(), eldsim.Config{
			Client: client, Truck: eldsim.NewTruck(route, plan, mem.OdometerMi, mem.EngineHours), Memory: mem,
			Clock: eldsim.RealClock{}, Speedup: speedup, Sample: sample, SendEvery: time.Minute,
			Heartbeat: time.Minute, Offline: offline, NotBefore: notBefore,
		})
		require.NoError(t, err)
		return stats
	}

	var trip *eldsim.Stats
	t.Run("a trip through a dead zone: nothing is lost or refused", func(t *testing.T) {
		trip = run(eldsim.DefaultTrip(route), 3900, 10*time.Second,
			[]eldsim.Window{{From: 10 * time.Minute, To: 15 * time.Minute}}, *me.InstalledAt)

		assert.Equal(t, 3900.0, trip.Speedup, "installed yesterday: full speed")
		assert.Positive(t, trip.Readings)
		assert.Equal(t, trip.Readings, trip.Accepted)
		assert.Zero(t, trip.Duplicates)
		assert.Zero(t, trip.Rejected, "every time and order rule holds for a well-behaved device")
		assert.GreaterOrEqual(t, trip.MaxBuffered, 25, "five minutes of readings waited out the dead zone")
		assert.Positive(t, trip.Heartbeats, "parked with the engine off at the fuel stop")
	})

	t.Run("the office sees the trip's end", func(t *testing.T) {
		var s struct {
			Connection         string        `json:"connection"`
			ClockOffsetSeconds *float64      `json:"clock_offset_seconds"`
			Position           *positionResp `json:"position"`
		}
		r := tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID+"/state", nil, http.StatusOK, &s)
		assert.Equal(t, "online", s.Connection)
		require.NotNil(t, s.ClockOffsetSeconds)
		assert.InDelta(t, 0, *s.ClockOffsetSeconds, 5, "the simulator's clock is the real one")
		require.NotNil(t, s.Position)
		end := route.Points[len(route.Points)-1]
		assert.InDelta(t, end.Lat, *s.Position.Latitude, 1e-6, "back at the end of the loop")
		assert.Contains(t, string(r.Body), `"odometer_mi":120034.7`, "120000 + the loop's 34.7 miles")

		var b struct {
			ELDDevice *struct {
				Position *positionResp `json:"position"`
			} `json:"eld_device"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks/"+truck.ID+"/current", nil, http.StatusOK, &b)
		require.NotNil(t, b.ELDDevice)
		require.NotNil(t, b.ELDDevice.Position, "the truck board shows it too")
	})

	t.Run("the history: every reading, on the truck, in order", func(t *testing.T) {
		// No office endpoint lists raw readings yet (the Phase 5 logbook
		// builds on them), so this one check reads the table.
		var total, onTruck, moving, odometerBack, gaps int
		require.NoError(t, testDB.QueryRow(t.Context(), `
			SELECT count(*), count(*) FILTER (WHERE truck_id = $2), count(*) FILTER (WHERE moving),
			       count(*) FILTER (WHERE odo_back), count(*) FILTER (WHERE seq_gap)
			FROM (SELECT truck_id, moving,
			             odometer_mi < lag(odometer_mi) OVER (ORDER BY seq) AS odo_back,
			             seq <> lag(seq) OVER (ORDER BY seq) + 1 AS seq_gap
			      FROM eld_events WHERE eld_device_id = $1) e`, eld.ID, truck.ID,
		).Scan(&total, &onTruck, &moving, &odometerBack, &gaps))

		assert.Equal(t, trip.Readings, total)
		assert.Equal(t, total, onTruck, "installed yesterday: every reading belongs to the truck")
		assert.Positive(t, moving)
		assert.Less(t, moving, total, "some while idling")
		assert.Zero(t, odometerBack, "the odometer never goes back")
		assert.Zero(t, gaps, "seq 1, 2, 3...: nothing missing, the dead zone included")
	})

	t.Run("the next trip continues the device's memory", func(t *testing.T) {
		lastSeq, lastOdo := mem.LastSeq, mem.OdometerMi
		// A short trip right after: it may not go back into the first
		// trip's time, so it runs at (about) real time: 5 seconds.
		short := []eldsim.Phase{{Kind: eldsim.Idle, Duration: 5 * time.Second}, {Kind: eldsim.Park}}
		began, notBefore := time.Now(), mem.LastRecordedAt
		second := run(short, 3900, time.Second, nil, mem.LastRecordedAt)
		took := time.Since(began)

		assert.Less(t, second.Speedup, 3900.0, "slowed: the first trip ended just now")
		assert.False(t, second.FirstRecorded.Before(notBefore), "starts after the first trip")
		expected := time.Duration(float64(5*time.Second) / second.Speedup)
		assert.Greater(t, took, expected*8/10,
			"slowed for real: the pace follows the fitted speedup (else readings run into the future)")
		assert.Positive(t, second.Accepted)
		assert.Zero(t, second.Duplicates, "seq continued: nothing looks resent")
		assert.Zero(t, second.Rejected, "nothing went back in time")
		assert.Greater(t, mem.LastSeq, lastSeq)
		assert.Equal(t, lastOdo, mem.OdometerMi, "idling adds no miles")
	})
}
