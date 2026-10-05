package e2e

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogin(t *testing.T) {
	tn := newTenant(t)

	t.Run("returns a bearer token pair", func(t *testing.T) {
		var p tokenPair
		anon.do(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"login": tn.AdminEmail, "password": userPassword}, http.StatusOK, &p)
		assert.Equal(t, "Bearer", p.TokenType)
		// Seconds left until expiry, so a second may already have passed.
		assert.InDelta(t, 15*60, p.ExpiresIn, 2, "ACCESS_TOKEN_TTL default")
		assert.InDelta(t, 30*24*3600, p.RefreshExpiresIn, 2, "REFRESH_TOKEN_TTL default")
	})

	t.Run("email is case-insensitive", func(t *testing.T) {
		login(t, strings.ToUpper(tn.AdminEmail), userPassword)
	})

	t.Run("wrong password and unknown login look the same", func(t *testing.T) {
		wrong := anon.fail(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"login": tn.AdminEmail, "password": "not the password"}, http.StatusUnauthorized)
		unknown := anon.fail(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"login": "nobody@e2e.test", "password": "not the password"}, http.StatusUnauthorized)
		assert.Equal(t, "unauthorized", wrong.Code)
		assert.Equal(t, wrong, unknown, "no answer reveals whether an account exists")
	})

	t.Run("records the last login time", func(t *testing.T) {
		var u userResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, &u)
		assert.NotNil(t, u.LastLoginAt)
	})
}

func TestAccessTokenRequired(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		r := anon.send(t, http.MethodGet, "/api/v1/trucks", nil)
		r.require(t, http.StatusUnauthorized)
		assert.Contains(t, r.Header.Get("WWW-Authenticate"), "Bearer")
	})

	t.Run("garbage token", func(t *testing.T) {
		r := client{token: "not.a.jwt"}.send(t, http.MethodGet, "/api/v1/trucks", nil)
		r.require(t, http.StatusUnauthorized)
		assert.Contains(t, r.Header.Get("WWW-Authenticate"), `error="invalid_token"`)
	})

	t.Run("token with a tampered signature", func(t *testing.T) {
		tok := platform(t).token
		tampered := tok[:len(tok)-2] + flip(tok[len(tok)-2:])
		client{token: tampered}.fail(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusUnauthorized)
	})

	t.Run("refresh token is not an access token", func(t *testing.T) {
		p := platform(t)
		client{token: p.refresh}.fail(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusUnauthorized)
	})
}

// flip changes every character of s, so a signature no longer matches.
func flip(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
	}
	return string(b)
}

func TestMe(t *testing.T) {
	t.Run("platform admin", func(t *testing.T) {
		var me userResp
		platform(t).do(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, &me)
		require.NotNil(t, me.Email)
		assert.Equal(t, adminEmail, *me.Email)
		assert.Nil(t, me.CompanyID, "platform users belong to no company")
		assert.Equal(t, []string{"platform_admin"}, me.Roles)
		assert.Contains(t, me.Permissions, "company:create")
		assert.Contains(t, me.Permissions, "ratelimit:update")
	})

	t.Run("company admin", func(t *testing.T) {
		tn := newTenant(t)
		var me userResp
		tn.Admin.do(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, &me)
		require.NotNil(t, me.CompanyID)
		assert.Equal(t, tn.ID, *me.CompanyID)
		assert.Equal(t, []string{"admin"}, me.Roles)
		assert.Contains(t, me.Permissions, "truck:create")
		assert.NotContains(t, me.Permissions, "company:create", "only the platform onboards companies")
	})

	t.Run("driver", func(t *testing.T) {
		_, drv := newTenant(t).newDriver(t)
		var me userResp
		drv.do(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, &me)
		assert.Equal(t, []string{"driver"}, me.Roles)
		assert.NotNil(t, me.Permissions, "[] rather than null")
	})
}

func TestRefreshAndLogout(t *testing.T) {
	tn := newTenant(t)

	refresh := func(t *testing.T, token string, want int) tokenPair {
		t.Helper()
		var p tokenPair
		r := anon.do(t, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": token}, want, nil)
		if want == http.StatusOK {
			r.decode(t, &p)
		}
		return p
	}

	t.Run("rotation: every refresh issues a new pair", func(t *testing.T) {
		s := login(t, tn.AdminEmail, userPassword)
		p := refresh(t, s.refresh, http.StatusOK)
		assert.NotEqual(t, s.refresh, p.RefreshToken)
		// The new access token works.
		client{token: p.AccessToken}.do(t, http.MethodGet, "/api/v1/auth/me", nil, http.StatusOK, nil)
	})

	t.Run("reuse of a rotated token revokes the whole session", func(t *testing.T) {
		s := login(t, tn.AdminEmail, userPassword)
		second := refresh(t, s.refresh, http.StatusOK)
		refresh(t, s.refresh, http.StatusUnauthorized) // looks like theft
		refresh(t, second.RefreshToken, http.StatusUnauthorized)
	})

	t.Run("reuse ends all of the user's sessions", func(t *testing.T) {
		// A stolen token may come from any device: the user logs in again
		// everywhere, the thief is locked out everywhere.
		other := login(t, tn.AdminEmail, userPassword)
		s := login(t, tn.AdminEmail, userPassword)
		refresh(t, s.refresh, http.StatusOK)
		refresh(t, s.refresh, http.StatusUnauthorized)
		refresh(t, other.refresh, http.StatusUnauthorized)
	})

	t.Run("unknown token", func(t *testing.T) {
		e := anon.fail(t, http.MethodPost, "/api/v1/auth/refresh",
			map[string]string{"refresh_token": "made-up"}, http.StatusUnauthorized)
		assert.Equal(t, "unauthorized", e.Code)
	})

	t.Run("logout ends only that session", func(t *testing.T) {
		other := login(t, tn.AdminEmail, userPassword)
		s := login(t, tn.AdminEmail, userPassword)
		anon.do(t, http.MethodPost, "/api/v1/auth/logout",
			map[string]string{"refresh_token": s.refresh}, http.StatusNoContent, nil)
		refresh(t, s.refresh, http.StatusUnauthorized)
		refresh(t, other.refresh, http.StatusOK)
	})

	t.Run("logout is idempotent", func(t *testing.T) {
		s := login(t, tn.AdminEmail, userPassword)
		for range 2 {
			anon.do(t, http.MethodPost, "/api/v1/auth/logout",
				map[string]string{"refresh_token": s.refresh}, http.StatusNoContent, nil)
		}
	})
}
