package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDriversCRUD(t *testing.T) {
	tn := newTenant(t)
	username := uniq("driver.")
	license := uniq("L")

	var d driverResp
	t.Run("create with a username only (drivers often have no email)", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": username, "password": userPassword, "first_name": "Bob", "last_name": "Lee",
			"license_number": license, "license_state": "TX", "license_expires_at": "2030-05-31",
		}, http.StatusCreated, &d)
		assert.Equal(t, tn.ID, d.CompanyID)
		require.NotNil(t, d.Username)
		assert.Equal(t, username, *d.Username)
		assert.Nil(t, d.Email)
		assert.Equal(t, "2030-05-31", d.LicenseExpiresAt)
		assert.True(t, d.IsActive)
	})
	require.NotEmpty(t, d.ID)

	t.Run("the account gets the driver role", func(t *testing.T) {
		var u userResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/users/"+d.UserID, nil, http.StatusOK, &u)
		assert.Equal(t, []string{"driver"}, u.Roles)
	})

	t.Run("validation", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": uniq("driver."), "password": userPassword, "first_name": "X", "last_name": "Y",
			"license_number": "has spaces!", "license_state": "XX", "license_expires_at": "2030-05-31",
		}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "license_number")
		assert.Contains(t, e.Details, "license_state")

		e = tn.Admin.fail(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": uniq("driver."), "password": userPassword, "first_name": "X", "last_name": "Y",
			"license_number": uniq("L"), "license_state": "TX", "license_expires_at": "31/05/2030",
		}, http.StatusBadRequest)
		assert.Equal(t, "must be a date in YYYY-MM-DD format", e.Details["license_expires_at"])
	})

	t.Run("username and license are unique", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": username, "password": userPassword, "first_name": "X", "last_name": "Y",
			"license_number": uniq("L"), "license_state": "TX", "license_expires_at": "2030-05-31",
		}, http.StatusConflict)
		tn.Admin.fail(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": uniq("driver."), "password": userPassword, "first_name": "X", "last_name": "Y",
			"license_number": license, "license_state": "TX", "license_expires_at": "2030-05-31",
		}, http.StatusConflict)
		// Same number from another state is a different license.
		tn.Admin.do(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": uniq("driver."), "password": userPassword, "first_name": "X", "last_name": "Y",
			"license_number": license, "license_state": "OK", "license_expires_at": "2030-05-31",
		}, http.StatusCreated, nil)
	})

	t.Run("get and list", func(t *testing.T) {
		var got driverResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers/"+d.ID, nil, http.StatusOK, &got)
		assert.Equal(t, d, got)

		var l page
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers", nil, http.StatusOK, &l)
		assert.Equal(t, 2, l.Total)
	})

	t.Run("update the license", func(t *testing.T) {
		// The same number in OK belongs to the driver created above.
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/drivers/"+d.ID,
			map[string]any{"license_state": "OK"}, http.StatusConflict)

		var got driverResp
		tn.Admin.do(t, http.MethodPatch, "/api/v1/drivers/"+d.ID, map[string]any{
			"license_state": "CA", "license_expires_at": "2031-01-15",
		}, http.StatusOK, &got)
		assert.Equal(t, "CA", got.LicenseState)
		assert.Equal(t, "2031-01-15", got.LicenseExpiresAt)
		assert.Equal(t, license, got.LicenseNumber)
	})

	t.Run("the driver reads their own profile", func(t *testing.T) {
		s := login(t, username, userPassword)
		var me driverResp
		s.do(t, http.MethodGet, "/api/v1/drivers/me", nil, http.StatusOK, &me)
		assert.Equal(t, d.ID, me.ID)
		// ...but not the driver list, or other drivers.
		s.fail(t, http.MethodGet, "/api/v1/drivers", nil, http.StatusForbidden)
		s.fail(t, http.MethodGet, "/api/v1/drivers/"+d.ID, nil, http.StatusForbidden)
	})

	t.Run("non-drivers have no driver profile", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, "/api/v1/drivers/me", nil, http.StatusNotFound)
	})

	t.Run("delete removes the profile and the login", func(t *testing.T) {
		s := login(t, username, userPassword)
		tn.Admin.do(t, http.MethodDelete, "/api/v1/drivers/"+d.ID, nil, http.StatusNoContent, nil)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/drivers/"+d.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/users/"+d.UserID, nil, http.StatusNotFound)
		anon.fail(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"login": username, "password": userPassword}, http.StatusUnauthorized)
		anon.fail(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": s.refresh}, http.StatusUnauthorized)
	})
}

func TestDriversPermissionsAndTenancy(t *testing.T) {
	tn := newTenant(t)
	other := newTenant(t)
	theirs, _ := other.newDriver(t)
	_, disp := tn.newUser(t, "dispatcher")
	_, mgr := tn.newUser(t, "manager")

	t.Run("dispatcher reads, manager manages", func(t *testing.T) {
		disp.do(t, http.MethodGet, "/api/v1/drivers", nil, http.StatusOK, nil)
		disp.fail(t, http.MethodPost, "/api/v1/drivers", map[string]any{}, http.StatusForbidden)
		mgr.do(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": uniq("driver."), "password": userPassword, "first_name": "M", "last_name": "D",
			"license_number": uniq("L"), "license_state": "CA", "license_expires_at": "2029-12-31",
		}, http.StatusCreated, nil)
	})

	t.Run("other company's drivers do not exist", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, "/api/v1/drivers/"+theirs.ID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/drivers/"+theirs.ID,
			map[string]any{"license_state": "CA"}, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/drivers/"+theirs.ID, nil, http.StatusNotFound)

		var l struct {
			Items []driverResp `json:"items"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/drivers?page_size=100", nil, http.StatusOK, &l)
		for _, d := range l.Items {
			assert.Equal(t, tn.ID, d.CompanyID)
		}
	})

	t.Run("platform admin creates drivers in any company, and must say which", func(t *testing.T) {
		p := platform(t)
		body := map[string]any{
			"username": uniq("driver."), "password": userPassword, "first_name": "P", "last_name": "D",
			"license_number": uniq("L"), "license_state": "NY", "license_expires_at": "2029-12-31",
		}
		e := p.fail(t, http.MethodPost, "/api/v1/drivers", body, http.StatusBadRequest)
		assert.Contains(t, e.Details, "company_id")

		body["company_id"] = other.ID
		var d driverResp
		p.do(t, http.MethodPost, "/api/v1/drivers", body, http.StatusCreated, &d)
		assert.Equal(t, other.ID, d.CompanyID)

		var l page
		p.do(t, http.MethodGet, "/api/v1/drivers?company_id="+other.ID, nil, http.StatusOK, &l)
		assert.Equal(t, 2, l.Total)
	})
}
