package e2e

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompaniesAsPlatform(t *testing.T) {
	p := platform(t)
	dot := fmt.Sprintf("%d", 3000000+next())

	var c companyResp
	t.Run("create", func(t *testing.T) {
		p.do(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Acme Freight LLC", "dot_number": dot, "timezone": "America/Denver",
		}, http.StatusCreated, &c)
		assert.Equal(t, "Acme Freight LLC", c.Name)
		assert.Equal(t, dot, c.DOTNumber)
		assert.Equal(t, "America/Denver", c.Timezone)
		assert.True(t, c.IsActive, "new companies are active")
	})
	require.NotEmpty(t, c.ID)

	t.Run("validation", func(t *testing.T) {
		e := p.fail(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "", "dot_number": "12AB", "timezone": "Mars/Olympus",
		}, http.StatusBadRequest)
		assert.Equal(t, "validation_failed", e.Code)
		assert.Contains(t, e.Details, "name")
		assert.Contains(t, e.Details, "dot_number")
		assert.Contains(t, e.Details, "timezone")
	})

	t.Run("DOT number is unique", func(t *testing.T) {
		e := p.fail(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Copycat", "dot_number": dot, "timezone": "America/Chicago",
		}, http.StatusConflict)
		assert.Equal(t, "conflict", e.Code)
	})

	t.Run("get", func(t *testing.T) {
		var got companyResp
		p.do(t, http.MethodGet, "/api/v1/companies/"+c.ID, nil, http.StatusOK, &got)
		assert.Equal(t, c, got)
	})

	t.Run("list", func(t *testing.T) {
		var l struct {
			Items []companyResp `json:"items"`
		}
		p.do(t, http.MethodGet, "/api/v1/companies?page_size=100", nil, http.StatusOK, &l)
		assert.Contains(t, l.Items, c)
	})

	t.Run("update", func(t *testing.T) {
		var got companyResp
		p.do(t, http.MethodPatch, "/api/v1/companies/"+c.ID, map[string]any{
			"name": "Acme Freight Inc", "is_active": false,
		}, http.StatusOK, &got)
		assert.Equal(t, "Acme Freight Inc", got.Name)
		assert.False(t, got.IsActive)
		assert.Equal(t, dot, got.DOTNumber, "fields left out stay unchanged")
	})

	t.Run("update needs at least one field", func(t *testing.T) {
		p.fail(t, http.MethodPatch, "/api/v1/companies/"+c.ID, map[string]any{}, http.StatusBadRequest)
	})

	t.Run("update to a taken DOT number", func(t *testing.T) {
		var other companyResp
		p.do(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Other", "dot_number": fmt.Sprintf("%d", 3000000+next()), "timezone": "UTC",
		}, http.StatusCreated, &other)
		p.fail(t, http.MethodPatch, "/api/v1/companies/"+other.ID,
			map[string]any{"dot_number": dot}, http.StatusConflict)
	})

	t.Run("delete", func(t *testing.T) {
		p.do(t, http.MethodDelete, "/api/v1/companies/"+c.ID, nil, http.StatusNoContent, nil)
		p.fail(t, http.MethodGet, "/api/v1/companies/"+c.ID, nil, http.StatusNotFound)
		p.fail(t, http.MethodDelete, "/api/v1/companies/"+c.ID, nil, http.StatusNotFound)
	})

	t.Run("a deleted company's DOT number can be reused", func(t *testing.T) {
		p.do(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Acme Reborn", "dot_number": dot, "timezone": "America/Chicago",
		}, http.StatusCreated, nil)
	})

	t.Run("unknown id", func(t *testing.T) {
		p.fail(t, http.MethodGet, "/api/v1/companies/01a10bf4-0000-7000-8000-000000000000", nil, http.StatusNotFound)
	})
}

func TestCompaniesAsCompanyUsers(t *testing.T) {
	tn := newTenant(t)
	other := newTenant(t)

	t.Run("admin sees only their own company", func(t *testing.T) {
		var l struct {
			page
			Items []companyResp `json:"items"`
		}
		tn.Admin.do(t, http.MethodGet, "/api/v1/companies", nil, http.StatusOK, &l)
		require.Equal(t, 1, l.Total)
		assert.Equal(t, tn.ID, l.Items[0].ID)

		tn.Admin.do(t, http.MethodGet, "/api/v1/companies/"+tn.ID, nil, http.StatusOK, nil)
		// 404, not 403: a 403 would confirm the id exists.
		e := tn.Admin.fail(t, http.MethodGet, "/api/v1/companies/"+other.ID, nil, http.StatusNotFound)
		assert.Equal(t, "not_found", e.Code)
	})

	t.Run("admin edits their own company only", func(t *testing.T) {
		var c companyResp
		tn.Admin.do(t, http.MethodPatch, "/api/v1/companies/"+tn.ID,
			map[string]any{"timezone": "America/Los_Angeles"}, http.StatusOK, &c)
		assert.Equal(t, "America/Los_Angeles", c.Timezone)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/companies/"+other.ID,
			map[string]any{"name": "Hijacked"}, http.StatusNotFound)
	})

	t.Run("only the platform creates and deletes companies", func(t *testing.T) {
		e := tn.Admin.fail(t, http.MethodPost, "/api/v1/companies", map[string]string{
			"name": "Sneaky", "dot_number": "9", "timezone": "UTC",
		}, http.StatusForbidden)
		assert.Equal(t, "forbidden", e.Code)
		tn.Admin.fail(t, http.MethodDelete, "/api/v1/companies/"+tn.ID, nil, http.StatusForbidden)
	})

	t.Run("dispatcher reads but does not edit", func(t *testing.T) {
		_, disp := tn.newUser(t, "dispatcher")
		disp.do(t, http.MethodGet, "/api/v1/companies/"+tn.ID, nil, http.StatusOK, nil)
		disp.fail(t, http.MethodPatch, "/api/v1/companies/"+tn.ID,
			map[string]any{"name": "Nope"}, http.StatusForbidden)
	})

	t.Run("driver has no company access", func(t *testing.T) {
		_, drv := tn.newDriver(t)
		drv.fail(t, http.MethodGet, "/api/v1/companies/"+tn.ID, nil, http.StatusForbidden)
	})
}
