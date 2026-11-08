package e2e

import (
	"net/http"
	"strings"
	"testing"

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

	t.Run("the office sees that the key is in use", func(t *testing.T) {
		var d struct {
			KeyLastUsedAt *string `json:"key_last_used_at"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID, nil, http.StatusOK, &d)
		assert.NotNil(t, d.KeyLastUsedAt)
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
