package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/ratelimit"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// These tests check the wiring: that routes exist and the middleware chain is
// active. Each middleware's own behaviour is tested in the middleware package.

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// failingCompanyService embeds the interface, so it satisfies it without
// implementing every method. Only List is overridden; calling any other
// method would panic (nil embedded interface), which is fine for this test.
type failingCompanyService struct {
	handler.CompanyService
}

func (failingCompanyService) List(context.Context, auth.Principal, service.ListParams) (*service.CompanyPage, error) {
	return nil, errors.New("connection reset: secret internal detail")
}

// fakeTokens accepts exactly one access token: "good-token".
type fakeTokens struct{}

func (fakeTokens) Parse(token string) (*auth.TokenClaims, error) {
	if token != "good-token" {
		return nil, errors.New("invalid")
	}
	return &auth.TokenClaims{UserID: uuid.New()}, nil
}

var authHeader = map[string]string{"Authorization": "Bearer good-token"}

// fakePermissions gives every caller the same fixed permission list.
type fakePermissions []model.Permission

func (f fakePermissions) GetPermissions(context.Context, uuid.UUID) ([]model.Permission, error) {
	return f, nil
}

func newTestRouter(buf *bytes.Buffer) *gin.Engine {
	return newTestRouterWith(buf, Options{}, model.AllPermissions())
}

func newTestRouterWithOptions(buf *bytes.Buffer, opts Options) *gin.Engine {
	return newTestRouterWith(buf, opts, model.AllPermissions())
}

// newTestRouterWith builds the real router; every caller gets perms.
func newTestRouterWith(buf *bytes.Buffer, opts Options, perms []model.Permission) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := logger.New(buf, true, "debug")
	sec := Security{Tokens: fakeTokens{}, Permissions: fakePermissions(perms), AuthLimiter: ratelimit.New(1000, time.Minute)}
	return NewRouter(log, opts, sec, Handlers{
		Health:    handler.NewHealthHandler(okPinger{}, log),
		Auth:      handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
		User:      handler.NewUserHandler(nil),
		Driver:    handler.NewDriverHandler(nil),
		Truck:     handler.NewTruckHandler(nil),
		ELDDevice: handler.NewELDDeviceHandler(nil),
		Company:   handler.NewCompanyHandler(failingCompanyService{}),
	})
}

func send(r http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRouter_ReadyRouteRegistered(t *testing.T) {
	r := newTestRouter(&bytes.Buffer{})

	assert.Equal(t, http.StatusOK, send(r, http.MethodGet, "/ready", nil).Code)
}

func TestRouter_HealthGoesThroughMiddleware(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	rec := send(r, http.MethodGet, "/health", nil)

	assert.Equal(t, http.StatusOK, rec.Code)

	requestID := rec.Header().Get(middleware.RequestIDHeader)
	assert.NotEmpty(t, requestID, "RequestID middleware must be active")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry), "RequestLogger must write one JSON line")
	assert.Equal(t, requestID, entry["request_id"])
	assert.Equal(t, "/health", entry["route"])
}

func TestRouter_UnknownRouteIs404(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	rec := send(r, http.MethodGet, "/does-not-exist", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"not_found","message":"route not found"}}`, rec.Body.String())
	assert.Contains(t, buf.String(), `"level":"WARN"`)
}

// publicRoutes is the complete list of routes that may be called WITHOUT an
// access token. Adding a route outside the protected group fails the next
// test unless it is consciously added here.
var publicRoutes = map[string]bool{
	"GET /health":               true,
	"GET /ready":                true,
	"POST /api/v1/auth/login":   true,
	"POST /api/v1/auth/refresh": true,
	"POST /api/v1/auth/logout":  true,
}

func TestRouter_EveryNonPublicRouteRequiresAuthentication(t *testing.T) {
	r := newTestRouter(&bytes.Buffer{})

	checked := 0
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if publicRoutes[key] {
			continue
		}
		// Fill path parameters like :id with a dummy value.
		path := strings.ReplaceAll(route.Path, ":id", uuid.NewString())

		rec := send(r, route.Method, path, nil)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s must require a token", key)
		checked++
	}
	assert.Greater(t, checked, 5, "sanity check: protected routes were found")
}

// authenticatedOnlyRoutes need a login but no specific permission.
var authenticatedOnlyRoutes = map[string]bool{
	"GET /api/v1/auth/me":    true,
	"GET /api/v1/drivers/me": true,
}

// A logged-in user with NO permissions must be denied (403) everywhere except
// public and login-only routes. A protected route that forgot its
// perm.Require(...) fails here.
func TestRouter_EveryProtectedRouteRequiresAPermission(t *testing.T) {
	r := newTestRouterWith(&bytes.Buffer{}, Options{}, nil) // valid token, zero permissions

	checked := 0
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if publicRoutes[key] || authenticatedOnlyRoutes[key] {
			continue
		}
		path := strings.ReplaceAll(route.Path, ":id", uuid.NewString())

		rec := send(r, route.Method, path, authHeader)

		assert.Equal(t, http.StatusForbidden, rec.Code, "%s must require a permission", key)
		checked++
	}
	assert.Greater(t, checked, 4, "sanity check: protected routes were found")
}

func TestRouter_CompanyRoutesRegistered(t *testing.T) {
	r := newTestRouter(&bytes.Buffer{})

	// With a valid token, an invalid UUID is rejected by the handler before
	// the service is called, so a 400 (not 401/404) proves the route exists
	// and authentication passed.
	rec := send(r, http.MethodGet, "/api/v1/companies/not-a-uuid", authHeader)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRouter_InternalErrorIsLoggedButHiddenFromClient(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	rec := send(r, http.MethodGet, "/api/v1/companies", authHeader)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret internal detail", "client must not see the cause")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "ERROR", entry["level"])
	assert.Contains(t, entry["errors"], "secret internal detail", "the log must contain the cause")
	assert.NotEmpty(t, entry["user_id"], "authenticated requests are logged with the user id")
}

func TestRouter_Swagger(t *testing.T) {
	t.Run("enabled serves UI and spec", func(t *testing.T) {
		r := newTestRouterWithOptions(&bytes.Buffer{}, Options{EnableSwagger: true})

		assert.Equal(t, http.StatusOK, send(r, http.MethodGet, "/swagger/index.html", nil).Code)

		spec := send(r, http.MethodGet, "/swagger/doc.json", nil)
		assert.Equal(t, http.StatusOK, spec.Code)
		assert.Contains(t, spec.Body.String(), "/api/v1/companies", "generated spec must include our routes")
		assert.Contains(t, spec.Body.String(), "BearerAuth", "spec must declare the bearer security scheme")
	})

	t.Run("disabled returns 404", func(t *testing.T) {
		r := newTestRouterWithOptions(&bytes.Buffer{}, Options{EnableSwagger: false})

		assert.Equal(t, http.StatusNotFound, send(r, http.MethodGet, "/swagger/index.html", nil).Code)
	})
}

func TestRouter_LoginRouteRegistered(t *testing.T) {
	r := newTestRouter(&bytes.Buffer{})

	// Broken JSON is rejected by the handler before the (nil) service is
	// called, so a 400 (not 404) proves the route exists.
	assert.Equal(t, http.StatusBadRequest, send(r, http.MethodPost, "/api/v1/auth/login", nil).Code)
}

func TestRouter_AuthRoutesAreRateLimitedByIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := logger.New(&bytes.Buffer{}, true, "debug")
	sec := Security{Tokens: fakeTokens{}, Permissions: fakePermissions(nil), AuthLimiter: ratelimit.New(1, time.Minute)}
	r := NewRouter(log, Options{}, sec, Handlers{
		Health:    handler.NewHealthHandler(okPinger{}, log),
		Auth:      handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
		User:      handler.NewUserHandler(nil),
		Driver:    handler.NewDriverHandler(nil),
		Truck:     handler.NewTruckHandler(nil),
		ELDDevice: handler.NewELDDeviceHandler(nil),
		Company:   handler.NewCompanyHandler(failingCompanyService{}),
	})

	// The body "{" is invalid, so the first call stops at 400 in the handler.
	assert.Equal(t, http.StatusBadRequest, send(r, http.MethodPost, "/api/v1/auth/login", nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, send(r, http.MethodPost, "/api/v1/auth/refresh", nil).Code,
		"one limit shared by all /auth routes")
	assert.Equal(t, http.StatusOK, send(r, http.MethodGet, "/health", nil).Code, "other routes are not limited")
}
