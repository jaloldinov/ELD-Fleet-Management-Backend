package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFleetJourney walks the main Phase 1 story through the running API:
// a platform admin onboards a carrier, the carrier's admin builds a fleet,
// a driver signs in, and nobody sees another company's data.
//
// The steps share state (tokens, ids), so they run in order; a failed step
// stops the journey, because the later steps would only fail confusingly.
func TestFleetJourney(t *testing.T) {
	var (
		admin      client // platform admin
		acmeAdmin  client // admin of company Acme
		driver     client // driver of Acme
		acmeID     string
		otherID    string // a second company, to check tenant isolation
		otherTruck string
		acmeTruck  string
		eldID      string
	)

	step := func(name string, fn func(t *testing.T)) {
		if !t.Run(name, fn) {
			t.FailNow()
		}
	}

	step("anonymous requests are refused", func(t *testing.T) {
		anon.do(t, http.MethodGet, "/health", nil, http.StatusOK, nil)
		assert.Equal(t, "unauthorized",
			anon.fail(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusUnauthorized).Code)
		assert.Equal(t, "unauthorized",
			anon.fail(t, http.MethodPost, "/api/v1/auth/login",
				map[string]string{"login": adminEmail, "password": "wrong password"}, http.StatusUnauthorized).Code)
	})

	step("platform admin created by the CLI can log in", func(t *testing.T) {
		admin = login(t, adminEmail, adminPassword)

		var me struct {
			Email     string   `json:"email"`
			CompanyID *string  `json:"company_id"`
			Roles     []string `json:"roles"`
		}
		admin.do(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, &me)
		assert.Equal(t, adminEmail, me.Email)
		assert.Nil(t, me.CompanyID, "platform users belong to no company")
		assert.Equal(t, []string{"platform_admin"}, me.Roles)
	})

	step("platform admin onboards companies", func(t *testing.T) {
		var c struct {
			ID string `json:"id"`
		}
		admin.do(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Acme Freight LLC", "dot_number": "1000001", "timezone": "America/Chicago",
		}, http.StatusCreated, &c)
		acmeID = c.ID

		admin.do(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Other Haulers Inc", "dot_number": "1000002", "timezone": "America/New_York",
		}, http.StatusCreated, &c)
		otherID = c.ID

		assert.Equal(t, "conflict", admin.fail(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Copycat", "dot_number": "1000001", "timezone": "America/Chicago",
		}, http.StatusConflict).Code, "DOT numbers are unique")

		// A truck in the other company, which Acme must never see.
		var tr struct {
			ID string `json:"id"`
		}
		admin.do(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"company_id": otherID, "unit_number": "900", "vin": "1HGCM82633A004352",
		}, http.StatusCreated, &tr)
		otherTruck = tr.ID

		admin.do(t, http.MethodPost, "/api/v1/users", map[string]any{
			"company_id": acmeID, "email": "admin@acme.test", "password": "acme admin password",
			"first_name": "Ada", "last_name": "Acme", "roles": []string{"admin"},
		}, http.StatusCreated, nil)
	})

	step("company admin builds the fleet", func(t *testing.T) {
		acmeAdmin = login(t, "admin@acme.test", "acme admin password")

		// No company_id: a company user always works in their own company.
		var tr struct {
			ID        string `json:"id"`
			CompanyID string `json:"company_id"`
		}
		acmeAdmin.do(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"unit_number": "101", "vin": "1M8GDM9AXKP042788", "make": "Freightliner",
			"license_plate": "ABC1234", "plate_state": "TX",
		}, http.StatusCreated, &tr)
		acmeTruck = tr.ID
		assert.Equal(t, acmeID, tr.CompanyID)

		var eld struct {
			ID string `json:"id"`
		}
		acmeAdmin.do(t, http.MethodPost, "/api/v1/eld-devices", map[string]any{
			"serial_number": "E2E-0001", "model": "PT30",
		}, http.StatusCreated, &eld)
		eldID = eld.ID

		var assigned struct {
			TruckID *string `json:"truck_id"`
		}
		acmeAdmin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eldID+"/truck",
			map[string]any{"truck_id": acmeTruck}, http.StatusOK, &assigned)
		require.NotNil(t, assigned.TruckID)
		assert.Equal(t, acmeTruck, *assigned.TruckID)

		var d struct {
			CompanyID string  `json:"company_id"`
			Username  *string `json:"username"`
		}
		acmeAdmin.do(t, http.MethodPost, "/api/v1/drivers", map[string]any{
			"username": "driver.bob", "password": "bob driver password",
			"first_name": "Bob", "last_name": "Lee",
			"license_number": "D1234567", "license_state": "TX", "license_expires_at": "2030-05-31",
		}, http.StatusCreated, &d)
		assert.Equal(t, acmeID, d.CompanyID)
	})

	step("company data is isolated from other companies", func(t *testing.T) {
		var trucks page
		acmeAdmin.do(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusOK, &trucks)
		assert.Equal(t, 1, trucks.Total, "only Acme's own truck is listed")

		// Another company's rows look like they do not exist: 404, not 403,
		// so ids cannot be probed.
		assert.Equal(t, "not_found",
			acmeAdmin.fail(t, http.MethodGet, "/api/v1/trucks/"+otherTruck, nil, http.StatusNotFound).Code)
		assert.Equal(t, "not_found",
			acmeAdmin.fail(t, http.MethodGet, "/api/v1/companies/"+otherID, nil, http.StatusNotFound).Code)
		// Cannot install Acme's ELD into the other company's truck either;
		// the answer is the same as for a truck that does not exist.
		var e struct {
			Error struct {
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		acmeAdmin.do(t, http.MethodPut, "/api/v1/eld-devices/"+eldID+"/truck",
			map[string]any{"truck_id": otherTruck}, http.StatusBadRequest, &e)
		assert.Equal(t, "truck not found", e.Error.Details["truck_id"])

		// Only the platform may create companies.
		assert.Equal(t, "forbidden",
			acmeAdmin.fail(t, http.MethodPost, "/api/v1/companies", map[string]string{
				"name": "Sneaky", "dot_number": "1000003", "timezone": "America/Chicago",
			}, http.StatusForbidden).Code)
	})

	step("driver logs in by username and sees only their own profile", func(t *testing.T) {
		driver = login(t, "driver.bob", "bob driver password")

		var me struct {
			CompanyID     string `json:"company_id"`
			LicenseNumber string `json:"license_number"`
		}
		driver.do(t, http.MethodGet, "/api/v1/drivers/me", nil, http.StatusOK, &me)
		assert.Equal(t, acmeID, me.CompanyID)
		assert.Equal(t, "D1234567", me.LicenseNumber)

		// Drivers have no fleet-management permissions.
		assert.Equal(t, "forbidden",
			driver.fail(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusForbidden).Code)
	})

	step("changes are recorded in the company's audit log", func(t *testing.T) {
		var logs page
		acmeAdmin.do(t, http.MethodGet, "/api/v1/audit-logs", nil, http.StatusOK, &logs)
		// At least: truck, ELD, ELD assignment, driver created by Acme's admin.
		assert.GreaterOrEqual(t, logs.Total, 4)
	})

	step("refresh tokens rotate and a reused one revokes the session", func(t *testing.T) {
		first := login(t, "admin@acme.test", "acme admin password")

		var second tokenPair
		anon.do(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": first.refresh}, http.StatusOK, &second)
		assert.NotEqual(t, first.refresh, second.RefreshToken, "every refresh issues a new token")

		// Using the old token again looks like theft: it is refused and the
		// whole family is revoked, so the newest token stops working too.
		anon.do(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": first.refresh}, http.StatusUnauthorized, nil)
		anon.do(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": second.RefreshToken}, http.StatusUnauthorized, nil)
	})

	step("logout ends the session", func(t *testing.T) {
		p := login(t, "driver.bob", "bob driver password")
		anon.do(t, http.MethodPost, "/api/v1/auth/logout",
			map[string]string{"refresh_token": p.refresh}, http.StatusNoContent, nil)
		anon.do(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": p.refresh}, http.StatusUnauthorized, nil)
	})
}
