package e2e

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rateLimitResp struct {
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Limit         int     `json:"limit"`
	WindowSeconds int     `json:"window_seconds"`
	UpdatedBy     *string `json:"updated_by"`
}

func TestRateLimitSettings(t *testing.T) {
	p := platform(t)

	t.Run("list and get", func(t *testing.T) {
		var l struct {
			Items []rateLimitResp `json:"items"`
		}
		p.do(t, http.MethodGet, "/api/v1/rate-limits", nil, http.StatusOK, &l)
		names := map[string]bool{}
		for _, rl := range l.Items {
			names[rl.Name] = true
		}
		assert.True(t, names["auth_requests"] && names["login_attempts"] && names["password_reset_requests"], "%v", names)

		var rl rateLimitResp
		p.do(t, http.MethodGet, "/api/v1/rate-limits/login_attempts", nil, http.StatusOK, &rl)
		assert.Equal(t, 5, rl.Limit)
		assert.Equal(t, 900, rl.WindowSeconds)
		p.fail(t, http.MethodGet, "/api/v1/rate-limits/no_such_rule", nil, http.StatusNotFound)
	})

	t.Run("validation", func(t *testing.T) {
		e := p.fail(t, http.MethodPatch, "/api/v1/rate-limits/login_attempts",
			map[string]any{"limit": 0, "window_seconds": 999999}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "limit")
		assert.Contains(t, e.Details, "window_seconds")
		p.fail(t, http.MethodPatch, "/api/v1/rate-limits/login_attempts", map[string]any{}, http.StatusBadRequest)
	})

	t.Run("company users cannot see or change platform settings", func(t *testing.T) {
		tn := newTenant(t)
		tn.Admin.fail(t, http.MethodGet, "/api/v1/rate-limits", nil, http.StatusForbidden)
		tn.Admin.fail(t, http.MethodPatch, "/api/v1/rate-limits/login_attempts",
			map[string]any{"limit": 10000}, http.StatusForbidden)
	})
}

// TestLoginRateLimit lowers login_attempts through the API and checks that
// the running server enforces the new value at once.
func TestLoginRateLimit(t *testing.T) {
	p := platform(t)
	tn := newTenant(t)
	const path = "/api/v1/rate-limits/login_attempts"

	var rl rateLimitResp
	p.do(t, http.MethodPatch, path, map[string]any{"limit": 2}, http.StatusOK, &rl)
	t.Cleanup(func() {
		p.do(t, http.MethodPatch, path, map[string]any{"limit": 5, "window_seconds": 900}, http.StatusOK, nil)
	})
	assert.Equal(t, 2, rl.Limit)
	require.NotNil(t, rl.UpdatedBy, "records who changed it")

	wrong := map[string]string{"login": tn.AdminEmail, "password": "guessing"}
	anon.fail(t, http.MethodPost, "/api/v1/auth/login", wrong, http.StatusUnauthorized)
	anon.fail(t, http.MethodPost, "/api/v1/auth/login", wrong, http.StatusUnauthorized)

	r := anon.send(t, http.MethodPost, "/api/v1/auth/login", wrong)
	r.require(t, http.StatusTooManyRequests)
	var e struct {
		Error apiError `json:"error"`
	}
	r.decode(t, &e)
	assert.Equal(t, "rate_limited", e.Error.Code)
	retry, err := strconv.Atoi(r.Header.Get("Retry-After"))
	require.NoError(t, err, "Retry-After is whole seconds")
	assert.Positive(t, retry)

	// Even the right password waits: the attacker must not be able to keep
	// guessing and recognise success.
	anon.fail(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"login": tn.AdminEmail, "password": userPassword}, http.StatusTooManyRequests)

	// The limit is per login name: another account is unaffected.
	_, s := tn.newUser(t, "dispatcher")
	assert.NotEmpty(t, s.token)

	// The change is in the audit log.
	var l auditList
	p.do(t, http.MethodGet, "/api/v1/audit-logs?action=rate_limit.update&entity_id=login_attempts", nil, http.StatusOK, &l)
	assert.GreaterOrEqual(t, l.Total, 1)
}
