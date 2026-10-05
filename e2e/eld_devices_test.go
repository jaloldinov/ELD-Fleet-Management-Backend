package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestELDDevicesCRUD(t *testing.T) {
	tn := newTenant(t)
	serial := uniq("PT30-")

	var d eldResp
	t.Run("create", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPost, "/api/v1/eld-devices", map[string]any{
			"serial_number": serial, "model": "PT30", "firmware_version": "1.2.3",
		}, http.StatusCreated, &d)
		assert.Equal(t, tn.ID, d.CompanyID)
		assert.Equal(t, serial, d.SerialNumber)
		assert.Nil(t, d.TruckID, "not installed yet")
		assert.True(t, d.IsActive)
	})
	require.NotEmpty(t, d.ID)

	t.Run("serial number is unique", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodPost, "/api/v1/eld-devices",
			map[string]any{"serial_number": serial}, http.StatusConflict)
		// Across companies too: a physical device exists once.
		newTenant(t).Admin.fail(t, http.MethodPost, "/api/v1/eld-devices",
			map[string]any{"serial_number": serial}, http.StatusConflict)
	})

	t.Run("validation", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPost, "/api/v1/eld-devices",
			map[string]any{"serial_number": ""}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "serial_number")
	})

	t.Run("get, update, list", func(t *testing.T) {
		var got eldResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+d.ID, nil, http.StatusOK, &got)
		assert.Equal(t, d, got)

		tn.Admin.do(t, http.MethodPatch, "/api/v1/eld-devices/"+d.ID,
			map[string]any{"firmware_version": "2.0.0"}, http.StatusOK, &got)
		assert.Equal(t, "2.0.0", got.FirmwareVersion)
		assert.Equal(t, "PT30", got.Model)

		var l page
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices", nil, http.StatusOK, &l)
		assert.Equal(t, 1, l.Total)
	})

	t.Run("delete", func(t *testing.T) {
		tn.Admin.do(t, http.MethodDelete, "/api/v1/eld-devices/"+d.ID, nil, http.StatusNoContent, nil)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/eld-devices/"+d.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/eld-devices/"+d.ID, nil, http.StatusNotFound)
	})
}

func TestELDInstallation(t *testing.T) {
	tn := newTenant(t)
	truck := tn.newTruck(t)
	eld := tn.newELD(t)
	install := func(c client, deviceID, truckID string, want int) eldResp {
		t.Helper()
		var d eldResp
		r := c.do(t, http.MethodPut, "/api/v1/eld-devices/"+deviceID+"/truck",
			map[string]any{"truck_id": truckID}, want, nil)
		if want == http.StatusOK {
			r.decode(t, &d)
		}
		return d
	}

	t.Run("install into a truck", func(t *testing.T) {
		d := install(tn.Admin, eld.ID, truck.ID, http.StatusOK)
		require.NotNil(t, d.TruckID)
		assert.Equal(t, truck.ID, *d.TruckID)

		var l struct {
			page
			Items []eldResp `json:"items"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices?truck_id="+truck.ID, nil, http.StatusOK, &l)
		require.Equal(t, 1, l.Total, "find the device of a truck")
		assert.Equal(t, eld.ID, l.Items[0].ID)
	})

	t.Run("one device per truck", func(t *testing.T) {
		second := tn.newELD(t)
		e := tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+second.ID+"/truck",
			map[string]any{"truck_id": truck.ID}, http.StatusConflict)
		assert.Equal(t, "conflict", e.Code)
	})

	t.Run("re-installing in the same truck is fine", func(t *testing.T) {
		install(tn.Admin, eld.ID, truck.ID, http.StatusOK)
	})

	t.Run("moving to another truck frees the first", func(t *testing.T) {
		other := tn.newTruck(t)
		d := install(tn.Admin, eld.ID, other.ID, http.StatusOK)
		assert.Equal(t, other.ID, *d.TruckID)
		install(tn.Admin, tn.newELD(t).ID, truck.ID, http.StatusOK)
		// Put things back for the next steps.
		var l struct {
			Items []eldResp `json:"items"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices?truck_id="+truck.ID, nil, http.StatusOK, &l)
		require.Len(t, l.Items, 1)
		tn.Admin.do(t, http.MethodDelete, "/api/v1/eld-devices/"+l.Items[0].ID+"/truck", nil, http.StatusOK, nil)
		install(tn.Admin, eld.ID, truck.ID, http.StatusOK)
	})

	t.Run("not into an out-of-service truck", func(t *testing.T) {
		parked := tn.newTruck(t)
		tn.Admin.do(t, http.MethodPatch, "/api/v1/trucks/"+parked.ID, map[string]any{"is_active": false}, http.StatusOK, nil)
		e := tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+tn.newELD(t).ID+"/truck",
			map[string]any{"truck_id": parked.ID}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "truck_id")
	})

	t.Run("an inactive device cannot be installed", func(t *testing.T) {
		broken := tn.newELD(t)
		tn.Admin.do(t, http.MethodPatch, "/api/v1/eld-devices/"+broken.ID, map[string]any{"is_active": false}, http.StatusOK, nil)
		tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+broken.ID+"/truck",
			map[string]any{"truck_id": tn.newTruck(t).ID}, http.StatusBadRequest)
	})

	t.Run("not into another company's truck", func(t *testing.T) {
		theirs := newTenant(t).newTruck(t)
		// Same answer as for a truck that does not exist: ids cannot be probed.
		e := tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+tn.newELD(t).ID+"/truck",
			map[string]any{"truck_id": theirs.ID}, http.StatusBadRequest)
		assert.Equal(t, "truck not found", e.Details["truck_id"])
		e = tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+tn.newELD(t).ID+"/truck",
			map[string]any{"truck_id": "01a10bf4-0000-7000-8000-000000000000"}, http.StatusBadRequest)
		assert.Equal(t, "truck not found", e.Details["truck_id"])
	})

	t.Run("uninstall", func(t *testing.T) {
		var d eldResp
		tn.Admin.do(t, http.MethodDelete, "/api/v1/eld-devices/"+eld.ID+"/truck", nil, http.StatusOK, &d)
		assert.Nil(t, d.TruckID)
		// Idempotent.
		tn.Admin.do(t, http.MethodDelete, "/api/v1/eld-devices/"+eld.ID+"/truck", nil, http.StatusOK, &d)
	})

	t.Run("deleting a truck uninstalls its device", func(t *testing.T) {
		install(tn.Admin, eld.ID, truck.ID, http.StatusOK)
		tn.Admin.do(t, http.MethodDelete, "/api/v1/trucks/"+truck.ID, nil, http.StatusNoContent, nil)
		var d eldResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/eld-devices/"+eld.ID, nil, http.StatusOK, &d)
		assert.Nil(t, d.TruckID)
	})
}

func TestELDDevicesPermissionsAndTenancy(t *testing.T) {
	tn := newTenant(t)
	other := newTenant(t)
	theirs := other.newELD(t)
	_, disp := tn.newUser(t, "dispatcher")

	t.Run("dispatcher reads only", func(t *testing.T) {
		disp.do(t, http.MethodGet, "/api/v1/eld-devices", nil, http.StatusOK, nil)
		disp.fail(t, http.MethodPost, "/api/v1/eld-devices", map[string]any{}, http.StatusForbidden)
		disp.fail(t, http.MethodPut, "/api/v1/eld-devices/"+theirs.ID+"/truck", map[string]any{}, http.StatusForbidden)
	})

	t.Run("other company's devices do not exist", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, "/api/v1/eld-devices/"+theirs.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/eld-devices/"+theirs.ID,
			map[string]any{"model": "X"}, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/eld-devices/"+theirs.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPut, "/api/v1/eld-devices/"+theirs.ID+"/truck",
			map[string]any{"truck_id": tn.newTruck(t).ID}, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/eld-devices/"+theirs.ID+"/truck", nil, http.StatusNotFound)
	})

	t.Run("platform admin registers devices for a company", func(t *testing.T) {
		var d eldResp
		platform(t).do(t, http.MethodPost, "/api/v1/eld-devices", map[string]any{
			"company_id": other.ID, "serial_number": uniq("SN-"),
		}, http.StatusCreated, &d)
		assert.Equal(t, other.ID, d.CompanyID)
	})
}
