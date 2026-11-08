package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deviceClient talks to the API as an ELD device: "Authorization: Device <key>".
func deviceClient(key string) client {
	return client{header: map[string]string{"Authorization": "Device " + key}}
}

type deviceSelf struct {
	DeviceID     string `json:"device_id"`
	SerialNumber string `json:"serial_number"`
	Truck        *struct {
		ID         string `json:"id"`
		UnitNumber string `json:"unit_number"`
	} `json:"truck"`
}

// TestDeviceKeys walks a device key's life: issued once, used, rotated,
// revoked; and checks that device keys and user tokens never cross.
func TestDeviceKeys(t *testing.T) {
	tn := newTenant(t)
	eld := tn.newELD(t)
	truck := tn.newTruck(t)
	issue := func() string {
		t.Helper()
		var res struct {
			DeviceKey string `json:"device_key"`
			ELDDevice struct {
				HasKey bool `json:"has_key"`
			} `json:"eld_device"`
		}
		r := tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &res)
		assert.Equal(t, "no-store", r.Header.Get("Cache-Control"))
		require.True(t, strings.HasPrefix(res.DeviceKey, "eldk_"))
		assert.True(t, res.ELDDevice.HasKey)
		return res.DeviceKey
	}

	key := issue()
	dev := deviceClient(key)

	t.Run("the device learns who it is and where it is installed", func(t *testing.T) {
		var me deviceSelf
		dev.do(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusOK, &me)
		assert.Equal(t, eld.ID, me.DeviceID)
		assert.Equal(t, eld.SerialNumber, me.SerialNumber)
		assert.Nil(t, me.Truck)

		tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
		dev.do(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusOK, &me)
		require.NotNil(t, me.Truck)
		assert.Equal(t, truck.UnitNumber, me.Truck.UnitNumber)
	})

	t.Run("the office sees that the device is talking", func(t *testing.T) {
		var d struct {
			LastSeenAt *string `json:"last_seen_at"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID, nil, http.StatusOK, &d)
		assert.NotNil(t, d.LastSeenAt)
	})

	t.Run("keys and user tokens never cross", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusUnauthorized)
		dev.fail(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusUnauthorized)
		dev.fail(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusUnauthorized)
		deviceClient("eldk_made-up").fail(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusUnauthorized)
	})

	t.Run("rotating stops the old key at once", func(t *testing.T) {
		newKey := issue()
		e := dev.fail(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusUnauthorized)
		assert.Equal(t, "invalid device key", e.Message)
		dev = deviceClient(newKey)
		dev.do(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusOK, nil)
	})

	t.Run("a retired device is told so", func(t *testing.T) {
		other := tn.newELD(t)
		var res struct {
			DeviceKey string `json:"device_key"`
		}
		tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+other.ID+"/credentials", nil, http.StatusCreated, &res)
		tn.Admin.do(t, http.MethodPatch, "/api/v1/eld-devices/"+other.ID, map[string]any{"status": "retired"}, http.StatusOK, nil)
		deviceClient(res.DeviceKey).fail(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusForbidden)
	})

	t.Run("revoking", func(t *testing.T) {
		tn.Admin.do(t, http.MethodDelete, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusOK, nil)
		dev.fail(t, http.MethodGet, "/api/v1/device/me", nil, http.StatusUnauthorized)
	})

	t.Run("only the device's own company may issue its key", func(t *testing.T) {
		newTenant(t).Admin.fail(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusNotFound)
	})
}

type ingestResp struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected   []struct {
		Index  int    `json:"index"`
		Seq    *int64 `json:"seq"`
		Reason string `json:"reason"`
	} `json:"rejected"`
}

func reading(seq int64, at string, speed float64) map[string]any {
	return map[string]any{"seq": seq, "recorded_at": at, "latitude": 32.7767, "longitude": -96.797,
		"speed_mph": speed, "odometer_mi": 120000.0 + float64(seq)/10, "engine_hours": 8123.4, "engine_on": true}
}

// TestDeviceTelemetry: a device sends readings; a lost reply is safe to
// resend; a bad reading is refused alone, with its reason.
func TestDeviceTelemetry(t *testing.T) {
	tn := newTenant(t)
	eld := tn.newELD(t)
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": tn.newTruck(t).ID}, http.StatusOK, nil)
	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)
	dev := deviceClient(issued.DeviceKey)
	send := func(events ...map[string]any) ingestResp {
		t.Helper()
		var r ingestResp
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": events}, http.StatusOK, &r)
		return r
	}
	batch := []map[string]any{
		reading(1, "2026-11-08T14:30:00Z", 0),
		reading(2, "2026-11-08T14:30:05Z", 12.5),
		reading(3, "2026-11-08T14:30:10Z", 61.5),
	}

	t.Run("a batch is stored", func(t *testing.T) {
		r := send(batch...)
		assert.Equal(t, 3, r.Accepted)
		assert.Zero(t, r.Duplicates)
		assert.Empty(t, r.Rejected)
	})

	t.Run("the reply was lost: sending again stores nothing twice", func(t *testing.T) {
		r := send(batch...)
		assert.Zero(t, r.Accepted)
		assert.Equal(t, 3, r.Duplicates)
	})

	t.Run("one bad reading is refused alone", func(t *testing.T) {
		bad := reading(5, "2026-11-08T14:30:20Z", 61.5)
		bad["latitude"] = 123.0
		r := send(reading(4, "2026-11-08T14:30:15Z", 60), bad, reading(6, "2026-11-08T14:30:25Z", 59))
		assert.Equal(t, 2, r.Accepted)
		require.Len(t, r.Rejected, 1)
		assert.Equal(t, 1, r.Rejected[0].Index)
		assert.Equal(t, int64(5), *r.Rejected[0].Seq)
		assert.Contains(t, r.Rejected[0].Reason, "latitude")
	})

	t.Run("an unusable request as a whole", func(t *testing.T) {
		dev.fail(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": []any{}}, http.StatusBadRequest)
		big := make([]map[string]any, 501)
		for i := range big {
			big[i] = reading(int64(1000+i), "2026-11-08T15:00:00Z", 50)
		}
		e := dev.fail(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": big}, http.StatusBadRequest)
		assert.Contains(t, e.Details["events"], "at most 500")
	})

	t.Run("only devices send readings", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": batch}, http.StatusUnauthorized)
	})
}

type positionResp struct {
	RecordedAt string   `json:"recorded_at"`
	Latitude   *float64 `json:"latitude"`
	SpeedMPH   float64  `json:"speed_mph"`
	Moving     bool     `json:"moving"`
	EngineOn   bool     `json:"engine_on"`
}

// TestDeviceState: the office sees where a truck is and whether its device
// is talking; readings sent late (after a tunnel) never move it back.
func TestDeviceState(t *testing.T) {
	tn := newTenant(t)
	eld := tn.newELD(t)
	truck := tn.newTruck(t)
	tn.Admin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eld.ID+"/truck", map[string]any{"truck_id": truck.ID}, http.StatusOK, nil)
	state := func() (string, *positionResp) {
		t.Helper()
		var s struct {
			Connection string        `json:"connection"`
			Position   *positionResp `json:"position"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID+"/state", nil, http.StatusOK, &s)
		return s.Connection, s.Position
	}

	t.Run("a device that never called", func(t *testing.T) {
		conn, pos := state()
		assert.Equal(t, "never", conn)
		assert.Nil(t, pos)
	})

	var issued struct {
		DeviceKey string `json:"device_key"`
	}
	tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices/"+eld.ID+"/credentials", nil, http.StatusCreated, &issued)
	dev := deviceClient(issued.DeviceKey)
	// Readings are stamped "now", so they fall inside the install stint.
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }

	t.Run("a heartbeat makes it online", func(t *testing.T) {
		dev.do(t, http.MethodPost, "/api/v1/device/heartbeat", nil, http.StatusNoContent, nil)
		conn, pos := state()
		assert.Equal(t, "online", conn)
		assert.Nil(t, pos, "contact, but no reading yet")
	})

	t.Run("the newest reading is the state", func(t *testing.T) {
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": []any{
			reading(6, at(2*time.Second), 61.5), reading(5, at(time.Second), 0),
		}}, http.StatusOK, nil)

		_, pos := state()
		require.NotNil(t, pos)
		assert.Equal(t, 61.5, pos.SpeedMPH, "seq 6 is newer, though it came first in the batch")
		assert.True(t, pos.Moving)
	})

	t.Run("a late retry goes to history, not to the state", func(t *testing.T) {
		var r ingestResp
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": []any{
			reading(3, at(-time.Minute), 0), // older seq, older time: recorded earlier, sent later
		}}, http.StatusOK, &r)
		assert.Equal(t, 1, r.Accepted)

		_, pos := state()
		assert.Equal(t, 61.5, pos.SpeedMPH, "the truck does not jump back")
	})

	t.Run("a clock that went backwards is refused", func(t *testing.T) {
		var r ingestResp
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{"events": []any{
			reading(7, at(-time.Hour), 0), // after seq 6, but an hour earlier
		}}, http.StatusOK, &r)
		require.Len(t, r.Rejected, 1)
		assert.Contains(t, r.Rejected[0].Reason, "clock went backwards")
	})

	t.Run("a reading from the future is refused; the drift is measured", func(t *testing.T) {
		var r ingestResp
		dev.do(t, http.MethodPost, "/api/v1/device/telemetry", map[string]any{
			"sent_at": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339Nano), // a clock 15 minutes fast
			"events":  []any{reading(8, time.Now().Add(15*time.Minute).UTC().Format(time.RFC3339Nano), 50)},
		}, http.StatusOK, &r)
		require.Len(t, r.Rejected, 1)
		assert.Contains(t, r.Rejected[0].Reason, "future")

		var s struct {
			ClockOffsetSeconds *float64 `json:"clock_offset_seconds"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID+"/state", nil, http.StatusOK, &s)
		require.NotNil(t, s.ClockOffsetSeconds)
		assert.InDelta(t, 900, *s.ClockOffsetSeconds, 5, "15 minutes fast: the office can see it")
	})

	t.Run("the truck board shows where the truck is", func(t *testing.T) {
		var b struct {
			ELDDevice *struct {
				Connection string        `json:"connection"`
				Position   *positionResp `json:"position"`
			} `json:"eld_device"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks/"+truck.ID+"/current", nil, http.StatusOK, &b)
		require.NotNil(t, b.ELDDevice)
		assert.Equal(t, "online", b.ELDDevice.Connection)
		require.NotNil(t, b.ELDDevice.Position)
		assert.NotNil(t, b.ELDDevice.Position.Latitude)
	})

	t.Run("another company sees nothing", func(t *testing.T) {
		newTenant(t).Admin.fail(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID+"/state", nil, http.StatusNotFound)
	})
}
