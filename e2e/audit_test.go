package e2e

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type auditResp struct {
	ID         string    `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Actor      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"actor"`
	CompanyID *string `json:"company_id"`
	Action    string  `json:"action"`
	Entity    string  `json:"entity"`
	EntityID  string  `json:"entity_id"`
	Changes   map[string]struct {
		Old any `json:"old"`
		New any `json:"new"`
	} `json:"changes"`
	RequestID string `json:"request_id"`
}

type auditList struct {
	page
	Items []auditResp `json:"items"`
}

func TestAuditLog(t *testing.T) {
	tn := newTenant(t)
	other := newTenant(t)

	// A change made with a known request ID, to follow it into the log.
	traced := tn.Admin
	traced.header = map[string]string{"X-Request-ID": uniq("e2e-audit-")}
	var tr truckResp
	traced.do(t, http.MethodPost, "/api/v1/trucks",
		map[string]any{"unit_number": uniq("A"), "vin": newVIN(t), "make": "Mack"}, http.StatusCreated, &tr)
	tn.Admin.do(t, http.MethodPatch, "/api/v1/trucks/"+tr.ID, map[string]any{"make": "Volvo"}, http.StatusOK, nil)
	tn.Admin.do(t, http.MethodDelete, "/api/v1/trucks/"+tr.ID, nil, http.StatusNoContent, nil)

	truckLog := func(t *testing.T, c client) auditList {
		t.Helper()
		var l auditList
		c.do(t, http.MethodGet, "/api/v1/audit-logs?entity=truck&entity_id="+tr.ID, nil, http.StatusOK, &l)
		return l
	}

	t.Run("records who changed what, newest first", func(t *testing.T) {
		l := truckLog(t, tn.Admin)
		require.Equal(t, 3, l.Total)
		actions := []string{l.Items[0].Action, l.Items[1].Action, l.Items[2].Action}
		assert.Equal(t, []string{"truck.delete", "truck.update", "truck.create"}, actions)

		update := l.Items[1]
		require.NotNil(t, update.Actor)
		assert.Equal(t, tn.AdminID, update.Actor.ID)
		require.NotNil(t, update.CompanyID)
		assert.Equal(t, tn.ID, *update.CompanyID)
		require.Contains(t, update.Changes, "make")
		assert.Equal(t, "Mack", update.Changes["make"].Old)
		assert.Equal(t, "Volvo", update.Changes["make"].New)

		assert.Equal(t, traced.header["X-Request-ID"], l.Items[2].RequestID, "the request ID links log lines and audit rows")
	})

	t.Run("filters", func(t *testing.T) {
		var l auditList
		tn.Admin.do(t, http.MethodGet, "/api/v1/audit-logs?action=truck.update&entity_id="+tr.ID+"&entity=truck",
			nil, http.StatusOK, &l)
		assert.Equal(t, 1, l.Total)

		tn.Admin.do(t, http.MethodGet, "/api/v1/audit-logs?actor_id="+tn.AdminID, nil, http.StatusOK, &l)
		for _, it := range l.Items {
			assert.Equal(t, tn.AdminID, it.Actor.ID)
		}

		future := url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))
		tn.Admin.do(t, http.MethodGet, "/api/v1/audit-logs?from="+future, nil, http.StatusOK, &l)
		assert.Equal(t, 0, l.Total)
	})

	t.Run("password changes are recorded without the password", func(t *testing.T) {
		u, _ := tn.newUser(t, "dispatcher")
		tn.Admin.do(t, http.MethodPut, "/api/v1/users/"+u.ID+"/password",
			map[string]string{"new_password": "a secret password"}, http.StatusNoContent, nil)
		r := tn.Admin.do(t, http.MethodGet, "/api/v1/audit-logs?entity=user&entity_id="+u.ID+"&action=user.set_password",
			nil, http.StatusOK, nil)
		var l auditList
		r.decode(t, &l)
		assert.Equal(t, 1, l.Total)
		assert.NotContains(t, string(r.Body), "a secret password")
	})

	t.Run("companies see only their own log", func(t *testing.T) {
		assert.Equal(t, 0, truckLog(t, other.Admin).Total)
		var l auditList
		other.Admin.do(t, http.MethodGet, "/api/v1/audit-logs?page_size=100&company_id="+tn.ID, nil, http.StatusOK, &l)
		for _, it := range l.Items {
			require.NotNil(t, it.CompanyID)
			assert.Equal(t, other.ID, *it.CompanyID, "company_id is ignored for company users")
		}
	})

	t.Run("platform sees every company", func(t *testing.T) {
		assert.Equal(t, 3, truckLog(t, platform(t)).Total)
	})

	t.Run("only admins read the log", func(t *testing.T) {
		_, mgr := tn.newUser(t, "manager")
		mgr.fail(t, http.MethodGet, "/api/v1/audit-logs", nil, http.StatusForbidden)
	})
}
