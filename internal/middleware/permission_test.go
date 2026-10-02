package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// fakeLoader returns fixed permissions and counts how often it is called.
type fakeLoader struct {
	perms []model.Permission
	err   error
	calls int
	gotID uuid.UUID
}

func (f *fakeLoader) GetPermissions(_ context.Context, id uuid.UUID) ([]model.Permission, error) {
	f.calls++
	f.gotID = id
	return f.perms, f.err
}

// withPrincipal simulates Authenticate.
func withPrincipal(p auth.Principal) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
	}
}

func permRouter(loader PermissionLoader, principal *auth.Principal, checks ...model.Permission) (*gin.Engine, *bool) {
	reached := false
	r := gin.New()
	if principal != nil {
		r.Use(withPrincipal(*principal))
	}
	pc := NewPermissionChecker(loader)
	handlers := []gin.HandlerFunc{}
	for _, perm := range checks {
		handlers = append(handlers, pc.Require(perm))
	}
	handlers = append(handlers, func(c *gin.Context) { reached = true; c.Status(http.StatusOK) })
	r.GET("/x", handlers...)
	return r, &reached
}

func TestRequire_Allowed(t *testing.T) {
	p := auth.Principal{UserID: uuid.New()}
	loader := &fakeLoader{perms: []model.Permission{model.PermTruckRead, model.PermTruckCreate}}
	r, reached := permRouter(loader, &p, model.PermTruckCreate)

	rec := doRequest(r, http.MethodGet, "/x", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, *reached)
	assert.Equal(t, p.UserID, loader.gotID, "permissions are loaded for the caller")
}

func TestRequire_Forbidden(t *testing.T) {
	p := auth.Principal{UserID: uuid.New()}
	loader := &fakeLoader{perms: []model.Permission{model.PermTruckRead}}
	r, reached := permRouter(loader, &p, model.PermTruckCreate)

	rec := doRequest(r, http.MethodGet, "/x", nil)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, *reached, "handler must not run")
	var body struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "forbidden", body.Error.Code)
	assert.Equal(t, "truck:create", body.Error.Details["permission"], "client learns what is missing")
}

func TestRequire_NoPermissionsAtAll(t *testing.T) {
	// e.g. a deactivated user whose access token is still valid.
	p := auth.Principal{UserID: uuid.New()}
	r, reached := permRouter(&fakeLoader{perms: nil}, &p, model.PermTruckRead)

	rec := doRequest(r, http.MethodGet, "/x", nil)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, *reached)
}

func TestRequire_LoadsPermissionsOncePerRequest(t *testing.T) {
	p := auth.Principal{UserID: uuid.New()}
	loader := &fakeLoader{perms: []model.Permission{model.PermTruckRead, model.PermTruckUpdate}}
	// Two checks on the same request (e.g. group-level + route-level).
	r, reached := permRouter(loader, &p, model.PermTruckRead, model.PermTruckUpdate)

	rec := doRequest(r, http.MethodGet, "/x", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, *reached)
	assert.Equal(t, 1, loader.calls, "second check must use the cached permissions")
}

func TestRequire_EmptyPermissionsAreCachedToo(t *testing.T) {
	p := auth.Principal{UserID: uuid.New()}
	loader := &fakeLoader{perms: nil}
	r := gin.New()
	r.Use(withPrincipal(p))
	pc := NewPermissionChecker(loader)
	// The first check fails and aborts; to observe caching we call the loader
	// path twice by running the permissions helper directly.
	r.GET("/x", func(c *gin.Context) {
		_, _ = pc.permissions(c, p.UserID)
		_, _ = pc.permissions(c, p.UserID)
		c.Status(http.StatusOK)
	})

	doRequest(r, http.MethodGet, "/x", nil)

	assert.Equal(t, 1, loader.calls, `"has no permissions" must not trigger a second query`)
}

func TestRequire_Errors(t *testing.T) {
	t.Run("no principal: route registered without Authenticate", func(t *testing.T) {
		loader := &fakeLoader{}
		r, reached := permRouter(loader, nil, model.PermTruckRead)

		rec := doRequest(r, http.MethodGet, "/x", nil)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "fail closed")
		assert.False(t, *reached)
		assert.Zero(t, loader.calls)
	})

	t.Run("loader failure", func(t *testing.T) {
		p := auth.Principal{UserID: uuid.New()}
		r, reached := permRouter(&fakeLoader{err: errors.New("db down")}, &p, model.PermTruckRead)

		rec := doRequest(r, http.MethodGet, "/x", nil)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.False(t, *reached, "on error, deny (fail closed)")
		assert.NotContains(t, rec.Body.String(), "db down")
	})
}
