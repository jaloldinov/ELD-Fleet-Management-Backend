package e2e

import (
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var codeRe = regexp.MustCompile(`\b(\d{6})\b`)

// TestPasswordReset is "forgot password" end to end: the code really goes
// out by email (to the fake SMTP server) and is typed back in.
func TestPasswordReset(t *testing.T) {
	tn := newTenant(t)
	u, oldSession := tn.newUser(t, "dispatcher")
	email := *u.Email
	const newPassword = "my brand new password"

	forgot := func(t *testing.T, addr string, want int) {
		t.Helper()
		anon.do(t, http.MethodPost, "/api/v1/auth/password/forgot", map[string]string{"email": addr}, want, nil)
	}

	var code string
	t.Run("forgot: the code is emailed", func(t *testing.T) {
		forgot(t, email, http.StatusAccepted)
		m := mailbox.waitFor(t, email, "password reset code")
		match := codeRe.FindStringSubmatch(m.Body)
		require.NotNil(t, match, "no 6-digit code in: %s", m.Body)
		code = match[1]
		assert.NotContains(t, m.Subject, code, "codes never go in the subject (lock screens, logs)")
		assert.Contains(t, m.Body, "Uma", "greets the user by name")
	})
	require.NotEmpty(t, code)

	t.Run("unknown address gets the same answer and no email", func(t *testing.T) {
		ghost := uniq("ghost") + "@e2e.test"
		forgot(t, ghost, http.StatusAccepted)
		time.Sleep(300 * time.Millisecond) // give a (wrong) email time to arrive
		assert.Zero(t, mailbox.count(ghost))
	})

	t.Run("malformed address", func(t *testing.T) {
		e := anon.fail(t, http.MethodPost, "/api/v1/auth/password/forgot",
			map[string]string{"email": "not-an-email"}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "email")
	})

	t.Run("wrong code", func(t *testing.T) {
		wrong := "000000"
		if code == wrong {
			wrong = "111111"
		}
		e := anon.fail(t, http.MethodPost, "/api/v1/auth/password/verify",
			map[string]string{"email": email, "code": wrong}, http.StatusBadRequest)
		assert.Equal(t, "invalid_reset_code", e.Code)
	})

	var resetToken string
	t.Run("verify: the right code gives a reset token", func(t *testing.T) {
		var v struct {
			ResetToken string `json:"reset_token"`
			ExpiresIn  int    `json:"expires_in"`
		}
		anon.do(t, http.MethodPost, "/api/v1/auth/password/verify",
			map[string]string{"email": email, "code": code}, http.StatusOK, &v)
		assert.NotEmpty(t, v.ResetToken)
		assert.Positive(t, v.ExpiresIn)
		resetToken = v.ResetToken
	})
	require.NotEmpty(t, resetToken)

	t.Run("a code works only once", func(t *testing.T) {
		e := anon.fail(t, http.MethodPost, "/api/v1/auth/password/verify",
			map[string]string{"email": email, "code": code}, http.StatusBadRequest)
		assert.Equal(t, "invalid_reset_code", e.Code)
	})

	t.Run("weak new password", func(t *testing.T) {
		e := anon.fail(t, http.MethodPost, "/api/v1/auth/password/reset",
			map[string]string{"reset_token": resetToken, "new_password": "short"}, http.StatusBadRequest)
		assert.Contains(t, e.Details, "new_password")
	})

	t.Run("reset", func(t *testing.T) {
		anon.do(t, http.MethodPost, "/api/v1/auth/password/reset",
			map[string]string{"reset_token": resetToken, "new_password": newPassword}, http.StatusNoContent, nil)

		// Logged out everywhere; only the new password works.
		anon.fail(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": oldSession.refresh}, http.StatusUnauthorized)
		anon.fail(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"login": email, "password": userPassword}, http.StatusUnauthorized)
		login(t, email, newPassword)

		// And the owner is told, in case it was not them.
		mailbox.waitFor(t, email, "password was changed")
	})

	t.Run("a reset token works only once", func(t *testing.T) {
		e := anon.fail(t, http.MethodPost, "/api/v1/auth/password/reset",
			map[string]string{"reset_token": resetToken, "new_password": "yet another password"}, http.StatusBadRequest)
		assert.Equal(t, "invalid_reset_token", e.Code)
	})

	t.Run("recorded in the audit log", func(t *testing.T) {
		var l auditList
		tn.Admin.do(t, http.MethodGet, "/api/v1/audit-logs?action=user.password_reset&entity_id="+u.ID+"&entity=user",
			nil, http.StatusOK, &l)
		assert.Equal(t, 1, l.Total)
	})

	t.Run("emails per address are rate limited", func(t *testing.T) {
		// 1 request above already counted; the seeded limit is 3 per hour.
		forgot(t, email, http.StatusAccepted)
		forgot(t, email, http.StatusAccepted)
		r := anon.send(t, http.MethodPost, "/api/v1/auth/password/forgot", map[string]string{"email": email})
		r.require(t, http.StatusTooManyRequests)
		assert.NotEmpty(t, r.Header.Get("Retry-After"))
	})
}

func TestPasswordResetForDisabledUser(t *testing.T) {
	tn := newTenant(t)
	u, _ := tn.newUser(t, "dispatcher")
	tn.Admin.do(t, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{"is_active": false}, http.StatusOK, nil)

	anon.do(t, http.MethodPost, "/api/v1/auth/password/forgot",
		map[string]string{"email": *u.Email}, http.StatusAccepted, nil)
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, mailbox.count(*u.Email), "disabled accounts get no code")
}
