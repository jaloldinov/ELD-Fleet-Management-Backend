package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsersCRUD(t *testing.T) {
	tn := newTenant(t)
	email := uniq("dispatcher") + "@e2e.test"

	var u userResp
	t.Run("create in own company (company_id comes from the token)", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPost, "/api/v1/users", map[string]any{
			"email": email, "password": userPassword, "first_name": "Dina", "last_name": "Spatch",
			"roles": []string{"dispatcher"},
		}, http.StatusCreated, &u)
		require.NotNil(t, u.CompanyID)
		assert.Equal(t, tn.ID, *u.CompanyID)
		assert.Equal(t, []string{"dispatcher"}, u.Roles)
		assert.True(t, u.IsActive)
		assert.Nil(t, u.LastLoginAt)
	})
	require.NotEmpty(t, u.ID)

	t.Run("validation", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPost, "/api/v1/users", map[string]any{
			"password": "short", "first_name": "", "last_name": "X", "roles": []string{"platform_admin"},
		}, http.StatusBadRequest)
		assert.Equal(t, "validation_failed", e.Code)
		assert.Contains(t, e.Details, "password")
		assert.Contains(t, e.Details, "first_name")
		assert.Contains(t, e.Details, "roles", "platform roles cannot be given to company users")
	})

	t.Run("email is unique (case-insensitive)", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPost, "/api/v1/users", map[string]any{
			"email": "  " + upper(email), "password": userPassword, "first_name": "Dup", "last_name": "Licate",
			"roles": []string{"dispatcher"},
		}, http.StatusConflict)
		assert.Equal(t, "conflict", e.Code)
	})

	t.Run("get and list", func(t *testing.T) {
		var got userResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/users/"+u.ID, nil, http.StatusOK, &got)
		assert.Equal(t, u, got)

		var l struct {
			page
			Items []userResp `json:"items"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/users", nil, http.StatusOK, &l)
		assert.Equal(t, 2, l.Total, "the admin and the dispatcher")
	})

	t.Run("update names and roles", func(t *testing.T) {
		var got userResp
		tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
			"first_name": "Dana", "roles": []string{"manager", "dispatcher"},
		}, http.StatusOK, &got)
		assert.Equal(t, "Dana", got.FirstName)
		assert.ElementsMatch(t, []string{"manager", "dispatcher"}, got.Roles)
	})

	t.Run("new roles apply to existing tokens at once", func(t *testing.T) {
		s := login(t, email, userPassword)
		s.do(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"unit_number": uniq("M"), "vin": newVIN(t),
		}, http.StatusCreated, nil) // manager may create trucks

		tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+u.ID,
			map[string]any{"roles": []string{"dispatcher"}}, http.StatusOK, nil)
		s.fail(t, http.MethodPost, "/api/v1/trucks", map[string]any{
			"unit_number": uniq("M"), "vin": newVIN(t),
		}, http.StatusForbidden) // same token, permission gone
	})

	t.Run("nobody changes their own roles, status or deletes themselves", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/users/"+tn.AdminID,
			map[string]any{"roles": []string{"admin", "manager"}}, http.StatusForbidden)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/users/"+tn.AdminID,
			map[string]any{"is_active": false}, http.StatusForbidden)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/users/"+tn.AdminID, nil, http.StatusForbidden)
		// Own name is fine.
		tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+tn.AdminID,
			map[string]any{"last_name": "Renamed"}, http.StatusOK, nil)
	})

	t.Run("delete", func(t *testing.T) {
		s := login(t, email, userPassword)
		tn.Admin.do(t, http.MethodDelete, "/api/v1/users/"+u.ID, nil, http.StatusNoContent, nil)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/users/"+u.ID, nil, http.StatusNotFound)
		// Deleted users can neither log in nor keep their session.
		anon.fail(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"login": email, "password": userPassword}, http.StatusUnauthorized)
		anon.fail(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": s.refresh}, http.StatusUnauthorized)
	})

	t.Run("a deleted user's email can be reused", func(t *testing.T) {
		tn.Admin.do(t, http.MethodPost, "/api/v1/users", map[string]any{
			"email": email, "password": userPassword, "first_name": "New", "last_name": "Person",
			"roles": []string{"dispatcher"},
		}, http.StatusCreated, nil)
	})
}

func TestUserDeactivation(t *testing.T) {
	tn := newTenant(t)
	u, s := tn.newUser(t, "manager")

	tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{"is_active": false}, http.StatusOK, nil)

	// The still-unexpired access token loses every permission immediately.
	s.fail(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusForbidden)
	// The session is revoked and logging in is refused.
	anon.fail(t, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": s.refresh}, http.StatusUnauthorized)
	e := anon.fail(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"login": *u.Email, "password": userPassword}, http.StatusForbidden)
	assert.Equal(t, "forbidden", e.Code)
	// Cannot set the password of a disabled account.
	tn.Admin.fail(t, http.MethodPut, "/api/v1/users/"+u.ID+"/password",
		map[string]string{"new_password": "a fresh password"}, http.StatusForbidden)

	// Reactivated: works again.
	tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{"is_active": true}, http.StatusOK, nil)
	login(t, *u.Email, userPassword).do(t, http.MethodGet, "/api/v1/trucks", nil, http.StatusOK, nil)
}

func TestUserSetPassword(t *testing.T) {
	tn := newTenant(t)
	u, s := tn.newUser(t, "dispatcher")
	path := "/api/v1/users/" + u.ID + "/password"

	e := tn.Admin.fail(t, http.MethodPut, path, map[string]string{"new_password": "short"}, http.StatusBadRequest)
	assert.Contains(t, e.Details, "new_password")

	tn.Admin.fail(t, http.MethodPut, "/api/v1/users/"+tn.AdminID+"/password",
		map[string]string{"new_password": "my own new password"}, http.StatusForbidden)

	_, disp := tn.newUser(t, "dispatcher")
	disp.fail(t, http.MethodPut, path, map[string]string{"new_password": "a fresh password"}, http.StatusForbidden)

	tn.Admin.do(t, http.MethodPut, path, map[string]string{"new_password": "a fresh password"}, http.StatusNoContent, nil)

	// Logged out everywhere; only the new password works.
	anon.fail(t, http.MethodPost, "/api/v1/auth/refresh",
		map[string]string{"refresh_token": s.refresh}, http.StatusUnauthorized)
	anon.fail(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"login": *u.Email, "password": userPassword}, http.StatusUnauthorized)
	login(t, *u.Email, "a fresh password")
}

func TestUsersPermissionsAndTenancy(t *testing.T) {
	tn := newTenant(t)
	other := newTenant(t)
	_, disp := tn.newUser(t, "dispatcher")
	mgrUser, mgr := tn.newUser(t, "manager")

	t.Run("dispatcher cannot read users", func(t *testing.T) {
		disp.fail(t, http.MethodGet, "/api/v1/users", nil, http.StatusForbidden)
	})

	t.Run("manager reads but does not manage users", func(t *testing.T) {
		mgr.do(t, http.MethodGet, "/api/v1/users", nil, http.StatusOK, nil)
		mgr.fail(t, http.MethodPost, "/api/v1/users", map[string]any{
			"email": uniq("x") + "@e2e.test", "password": userPassword, "first_name": "X", "last_name": "Y",
			"roles": []string{"admin"},
		}, http.StatusForbidden)
		mgr.fail(t, http.MethodPut, "/api/v1/users/"+tn.AdminID+"/password",
			map[string]string{"new_password": "taking over now"}, http.StatusForbidden)
	})

	t.Run("other company's users do not exist for this admin", func(t *testing.T) {
		tn.Admin.fail(t, http.MethodGet, "/api/v1/users/"+other.AdminID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/users/"+other.AdminID,
			map[string]any{"first_name": "Hijacked"}, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/users/"+other.AdminID, nil, http.StatusNotFound)
		tn.Admin.fail(t, http.MethodPut, "/api/v1/users/"+other.AdminID+"/password",
			map[string]string{"new_password": "taking over now"}, http.StatusNotFound)
	})

	t.Run("company_id in the body cannot create users elsewhere", func(t *testing.T) {
		// That company "does not exist" for this admin.
		tn.Admin.fail(t, http.MethodPost, "/api/v1/users", map[string]any{
			"company_id": other.ID, "email": uniq("x") + "@e2e.test", "password": userPassword,
			"first_name": "X", "last_name": "Y", "roles": []string{"dispatcher"},
		}, http.StatusNotFound)
		var l page
		other.Admin.do(t, http.MethodGet, "/api/v1/users", nil, http.StatusOK, &l)
		assert.Equal(t, 1, l.Total, "nothing was added to the other company")
	})

	t.Run("platform admin lists users across companies, filtered by company", func(t *testing.T) {
		var l struct {
			page
			Items []userResp `json:"items"`
		}
		platform(t).do(t, http.MethodGet, "/api/v1/users?company_id="+tn.ID+"&page_size=100", nil, http.StatusOK, &l)
		ids := make([]string, 0, len(l.Items))
		for _, u := range l.Items {
			require.NotNil(t, u.CompanyID)
			assert.Equal(t, tn.ID, *u.CompanyID)
			ids = append(ids, u.ID)
		}
		assert.Contains(t, ids, mgrUser.ID)
	})

	t.Run("platform admin creates platform users", func(t *testing.T) {
		var u userResp
		platform(t).do(t, http.MethodPost, "/api/v1/users", map[string]any{
			"email": uniq("staff") + "@e2e.test", "password": userPassword,
			"first_name": "Staff", "last_name": "Member", "roles": []string{"platform_admin"},
		}, http.StatusCreated, &u)
		assert.Nil(t, u.CompanyID)
	})
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
