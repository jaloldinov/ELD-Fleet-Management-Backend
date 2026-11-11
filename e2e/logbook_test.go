package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- Phase 5 response shapes (the JSON contract) ----------

type dutyEventResp struct {
	ID          string   `json:"id"`
	DriverID    string   `json:"driver_id"`
	Status      string   `json:"status"`
	Special     *string  `json:"special"`
	StartedAt   string   `json:"started_at"`
	Origin      string   `json:"origin"`
	TruckID     *string  `json:"truck_id"`
	Latitude    *float64 `json:"latitude"`
	OdometerMi  *float64 `json:"odometer_mi"`
	EngineHours *float64 `json:"engine_hours"`
	Note        *string  `json:"note"`
}

type myDutyResp struct {
	Current *dutyEventResp `json:"current"`
}

// TestDriverLogbook: a driver presses status buttons in the app. Off the
// truck a change has no truck; on it, where it happened comes from the
// truck's ELD. Pressing the same button twice changes nothing, and driving
// can never be chosen.
func TestDriverLogbook(t *testing.T) {
	tn := newTenant(t)
	ali, aliC := tn.newDriver(t)
	truck, eld := tn.newTruck(t), tn.newELD(t)
	const path = "/api/v1/drivers/me/duty-status"

	// Status changes are recorded in whole seconds, and two in the same
	// second are refused: wait for the next one between changes.
	press := func(t *testing.T, body map[string]any, want int) dutyEventResp {
		t.Helper()
		time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
		var e dutyEventResp
		aliC.do(t, http.MethodPost, path, body, want, &e)
		return e
	}

	t.Run("nothing recorded yet", func(t *testing.T) {
		var me myDutyResp
		aliC.do(t, http.MethodGet, path, nil, http.StatusOK, &me)
		assert.Nil(t, me.Current)
	})

	t.Run("on no truck: the change has no truck", func(t *testing.T) {
		e := press(t, map[string]any{"status": "on_duty", "note": "  paperwork at the terminal "}, http.StatusCreated)
		assert.Equal(t, ali.ID, e.DriverID)
		assert.Equal(t, "on_duty", e.Status)
		assert.Equal(t, "driver", e.Origin)
		assert.Nil(t, e.TruckID)
		assert.Equal(t, "paperwork at the terminal", *e.Note)
	})

	// The ELD goes in the truck and reports once; Ali takes the truck.
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)
	var ingest ingestResp
	deviceClient(issued.DeviceKey).do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{
		"events": []map[string]any{reading(1, time.Now().UTC().Format(time.RFC3339Nano), 0)},
	}, http.StatusOK, &ingest)
	require.Equal(t, 1, ingest.Accepted)
	aliC.do(t, http.MethodPut, "/api/v1/drivers/me/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)

	var sleeper dutyEventResp
	t.Run("on a truck: truck, position and odometer from its ELD", func(t *testing.T) {
		sleeper = press(t, map[string]any{"status": "sleeper_berth"}, http.StatusCreated)
		require.NotNil(t, sleeper.TruckID)
		assert.Equal(t, truck.ID, *sleeper.TruckID)
		require.NotNil(t, sleeper.OdometerMi)
		assert.InDelta(t, 120000.1, *sleeper.OdometerMi, 0.001, "the reading's odometer")
		assert.NotNil(t, sleeper.Latitude)
	})

	t.Run("the same button again changes nothing", func(t *testing.T) {
		again := press(t, map[string]any{"status": "sleeper_berth"}, http.StatusOK)
		assert.Equal(t, sleeper.ID, again.ID)
	})

	t.Run("personal conveyance: off duty, on the truck", func(t *testing.T) {
		e := press(t, map[string]any{"status": "off_duty", "special": "personal_conveyance"}, http.StatusCreated)
		assert.Equal(t, "personal_conveyance", *e.Special)

		var me myDutyResp
		aliC.do(t, http.MethodGet, path, nil, http.StatusOK, &me)
		require.NotNil(t, me.Current)
		assert.Equal(t, e.ID, me.Current.ID, "the latest change is in force")
	})

	t.Run("refused", func(t *testing.T) {
		e := aliC.fail(t, http.MethodPost, path, map[string]any{"status": "driving"}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "status", "driving comes from the ELD")
		e = aliC.fail(t, http.MethodPost, path, map[string]any{"status": "on_duty", "special": "personal_conveyance"}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "special")
		aliC.fail(t, http.MethodPost, path, map[string]any{"status": "on_duty", "when": "yesterday"}, http.StatusBadRequest)
	})

	t.Run("only drivers have a logbook", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, path, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPost, path, map[string]any{"status": "on_duty"}, http.StatusNotFound)
	})
}

// TestAutomaticDriving: the truck's ELD, not the driver, writes "driving".
// The truck moves → Ali is driving; it stands 5 minutes → Ali is on duty;
// while it moves, Ali cannot change the status by hand.
func TestAutomaticDriving(t *testing.T) {
	tn := newTenant(t)
	ali, aliC := tn.newDriver(t)
	truck, eld := tn.newTruck(t), tn.newELD(t)
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	aliC.do(t, http.MethodPut, "/api/v1/drivers/me/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)
	dev := deviceClient(issued.DeviceKey)

	// Fixture, not API: "the ELD was installed and Ali took the truck an
	// hour ago", so the trip below can be in the past (both are always
	// "now" through the API).
	_, err := testDB.Exec(t.Context(),
		`UPDATE eld_truck_assignments SET started_at = now() - interval '1 hour' WHERE eld_device_id = $1`, eld.ID)
	require.NoError(t, err)
	_, err = testDB.Exec(t.Context(),
		`UPDATE driver_truck_assignments SET started_at = now() - interval '1 hour' WHERE driver_id = $1`, ali.ID)
	require.NoError(t, err)

	send := func(t *testing.T, points ...map[string]any) {
		t.Helper()
		var r ingestResp
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": points}, http.StatusOK, &r)
		require.Equal(t, len(points), r.Accepted, "rejected: %+v", r.Rejected)
	}
	logbook := func(t *testing.T) []string {
		t.Helper()
		rows, err := testDB.Query(t.Context(), `
			SELECT status, origin, started_at FROM duty_status_events WHERE driver_id = $1 ORDER BY started_at`, ali.ID)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var status, origin string
			var at time.Time
			require.NoError(t, rows.Scan(&status, &origin, &at))
			out = append(out, status+"/"+origin+"@"+at.UTC().Format(time.TimeOnly))
		}
		return out
	}
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	clock := func(d time.Duration) string { return base.Add(d).Format(time.TimeOnly) }

	t.Run("moving is driving; standing 5 minutes is on duty", func(t *testing.T) {
		send(t,
			reading(1, ts(0), 0),
			reading(2, ts(time.Minute), 40), // leaves the yard
			reading(3, ts(2*time.Minute), 62),
			reading(4, ts(20*time.Minute), 0), // stops at the customer
			reading(5, ts(26*time.Minute), 0),
		)

		assert.Equal(t, []string{
			"driving/auto@" + clock(time.Minute),
			"on_duty/auto@" + clock(25*time.Minute),
		}, logbook(t))
		var me myDutyResp
		aliC.do(t, http.MethodGet, "/api/v1/drivers/me/duty-status", nil, http.StatusOK, &me)
		require.NotNil(t, me.Current)
		assert.Equal(t, "on_duty", me.Current.Status)
		assert.Equal(t, "auto", me.Current.Origin)
		require.NotNil(t, me.Current.TruckID)
		assert.Equal(t, truck.ID, *me.Current.TruckID)
	})

	t.Run("while the truck moves, the driver cannot change it", func(t *testing.T) {
		send(t, reading(6, time.Now().UTC().Format(time.RFC3339Nano), 55))

		var me myDutyResp
		aliC.do(t, http.MethodGet, "/api/v1/drivers/me/duty-status", nil, http.StatusOK, &me)
		assert.Equal(t, "driving", me.Current.Status)
		e := aliC.fail(t, http.MethodPost, "/api/v1/drivers/me/duty-status", map[string]any{"status": "off_duty"}, http.StatusConflict)
		assert.Contains(t, e.Message, "moving")
	})

	t.Run("sending the same readings again writes nothing twice", func(t *testing.T) {
		before := logbook(t)
		var r ingestResp
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": []map[string]any{
			reading(2, ts(time.Minute), 40), reading(5, ts(26*time.Minute), 0),
		}}, http.StatusOK, &r)
		assert.Equal(t, 2, r.Duplicates)
		assert.Equal(t, before, logbook(t))
	})
}
