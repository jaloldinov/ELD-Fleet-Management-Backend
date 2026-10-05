package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrucksCRUD(t *testing.T) {
	tn := newTenant(t)
	vin := newVIN(t)
	unit := uniq("T")

	var tr truckResp
	t.Run("create", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"unit_number": unit, "vin": strings.ToLower(vin), "make": "Freightliner", "model": "Cascadia",
			"year": 2022, "license_plate": "ABC1234", "plate_state": "TX",
		}, http.StatusCreated, &tr)
		assert.Equal(t, tn.ID, tr.CompanyID)
		assert.Equal(t, vin, tr.VIN, "VINs are normalized to upper case")
		require.NotNil(t, tr.Year)
		assert.Equal(t, 2022, *tr.Year)
		assert.True(t, tr.IsActive)
	})
	require.NotEmpty(t, tr.ID)

	t.Run("validation", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"unit_number": "", "vin": "1M8GDM9A1KP042788", // wrong check digit
			"year": 1970, "license_plate": "ABC1234", // plate without state
		}, http.StatusBadRequest)
		for _, f := range []string{"unit_number", "vin", "year", "license_plate"} {
			assert.Contains(t, e.Details, f)
		}
	})

	t.Run("VIN is unique everywhere, unit number within the fleet", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodPost, "/api/v1/trucks",
			map[string]any{"unit_number": uniq("T"), "vin": vin}, http.StatusConflict)
		tn.Admin.fail(t, http.MethodPost, "/api/v1/trucks",
			map[string]any{"unit_number": unit, "vin": newVIN(t)}, http.StatusConflict)
		// Another company may use the same unit number.
		newTenant(t).Admin.do(t, http.MethodPost, "/api/v1/trucks",
			map[string]any{"unit_number": unit, "vin": newVIN(t)}, http.StatusCreated, nil)
	})

	t.Run("get", func(t *testing.T) {
		var got truckResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks/"+tr.ID, nil, http.StatusOK, &got)
		assert.Equal(t, tr, got)
	})

	t.Run("update and filter by active", func(t *testing.T) {
		var got truckResp
		tn.Admin.do(t, http.MethodPatch, "/api/v1/trucks/"+tr.ID, map[string]any{
			"make": "Volvo", "is_active": false,
		}, http.StatusOK, &got)
		assert.Equal(t, "Volvo", got.Make)
		assert.False(t, got.IsActive)

		spare := tn.newTruck(t)
		var active, inactive struct {
			page
			Items []truckResp `json:"items"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks?is_active=true", nil, http.StatusOK, &active)
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks?is_active=false", nil, http.StatusOK, &inactive)
		require.Equal(t, 1, active.Total)
		require.Equal(t, 1, inactive.Total)
		assert.Equal(t, spare.ID, active.Items[0].ID)
		assert.Equal(t, tr.ID, inactive.Items[0].ID)
	})

	t.Run("VIN cannot be changed", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPatch, "/api/v1/trucks/"+tr.ID,
			map[string]any{"vin": newVIN(t)}, http.StatusBadRequest)
		assert.Equal(t, "unknown field", e.Details["vin"])
	})

	t.Run("delete", func(t *testing.T) {
		tn.Admin.do(t, http.MethodDelete, "/api/v1/trucks/"+tr.ID, nil, http.StatusNoContent, nil)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/trucks/"+tr.ID, nil, http.StatusNotFound)
		// Its VIN is free again (e.g. the truck was sold and registered anew).
		tn.Admin.do(t, http.MethodPost, "/api/v1/trucks",
			map[string]any{"unit_number": uniq("T"), "vin": vin}, http.StatusCreated, nil)
	})
}

func TestTrucksPermissionsAndTenancy(t *testing.T) {
	tn := newTenant(t)
	other := newTenant(t)
	theirs := other.newTruck(t)
	_, disp := tn.newUser(t, "dispatcher")
	_, drv := tn.newDriver(t)

	t.Run("dispatcher reads only; driver nothing", func(t *testing.T) {
		disp.do(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusOK, nil)
		disp.fail(t, http.MethodPost, "/api/v1/trucks", map[string]any{}, http.StatusForbidden)
		drv.fail(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusForbidden)
	})

	t.Run("other company's trucks do not exist", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, "/api/v1/trucks/"+theirs.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/trucks/"+theirs.ID,
			map[string]any{"make": "Stolen"}, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/trucks/"+theirs.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"company_id": other.ID, "unit_number": uniq("T"), "vin": newVIN(t),
		}, http.StatusNotFound)
		// company_id filter is ignored for company users.
		var l page
		tn.Admin.do(t, http.MethodGet, "/api/v1/trucks?company_id="+other.ID, nil, http.StatusOK, &l)
		assert.Equal(t, 0, l.Total)
	})

	t.Run("platform admin works in any company", func(t *testing.T) {
		p := platform(t)
		e := p.fail(t, http.MethodPost, "/api/v1/trucks",
			map[string]any{"unit_number": uniq("T"), "vin": newVIN(t)}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "company_id")

		p.do(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"company_id": other.ID, "unit_number": uniq("T"), "vin": newVIN(t),
		}, http.StatusCreated, nil)
		var l page
		p.do(t, http.MethodGet, "/api/v1/trucks?company_id="+other.ID, nil, http.StatusOK, &l)
		assert.Equal(t, 2, l.Total)
	})
}
