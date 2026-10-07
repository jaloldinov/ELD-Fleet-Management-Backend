package handler_test

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

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/ratelimit"
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
	return newTestRouterWith(buf, handler.RouterOptions{}, model.AllPermissions())
}

func newTestRouterWithOptions(buf *bytes.Buffer, opts handler.RouterOptions) *gin.Engine {
	return newTestRouterWith(buf, opts, model.AllPermissions())
}

// newTestRouterWith builds the real router; every caller gets perms.
func newTestRouterWith(buf *bytes.Buffer, opts handler.RouterOptions, perms []model.Permission) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := logger.New(buf, true, "debug")
	sec := handler.RouterSecurity{Tokens: fakeTokens{}, Permissions: fakePermissions(perms), AuthLimiter: ratelimit.New(1000, time.Minute)}
	return handler.NewRouter(log, opts, sec, handler.Handlers{
		Health:    handler.NewHealthHandler(okPinger{}, log),
		Auth:      handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
		User:      handler.NewUserHandler(nil),
		Driver:    handler.NewDriverHandler(nil),
		Truck:     handler.NewTruckHandler(nil),
		ELDDevice: handler.NewELDDeviceHandler(nil),
		Company:   handler.NewCompanyHandler(failingCompanyService{}),
		RateLimit: handler.NewRateLimitHandler(nil),
		Password:  handler.NewPasswordResetHandler(nil, nil),
		AuditLog:  handler.NewAuditLogHandler(nil),
		// Billing on, so the generic route checks below cover its routes too.
		Product:       handler.NewProductHandler(nil),
		PaymentMethod: handler.NewPaymentMethodHandler(nil),
		Subscription:  handler.NewSubscriptionHandler(nil),
		Webhook:       handler.NewWebhookHandler(nil),
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

	"POST /api/v1/auth/password/forgot": true,
	"POST /api/v1/auth/password/verify": true,
	"POST /api/v1/auth/password/reset":  true,

	// The frontend's files: the page itself logs in, then calls the API.
	"GET /":               true,
	"GET /app/*filepath":  true,
	"HEAD /app/*filepath": true,

	// Stripe signs its webhooks instead of sending a token.
	"POST /api/v1/webhooks/stripe": true,
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
	"GET /api/v1/auth/me":        true,
	"GET /api/v1/drivers/me":     true,
	"GET /api/v1/billing/config": true,
}

// A logged-in user with NO permissions must be denied (403) everywhere except
// public and login-only routes. A protected route that forgot its
// perm.Require(...) fails here.
func TestRouter_EveryProtectedRouteRequiresAPermission(t *testing.T) {
	r := newTestRouterWith(&bytes.Buffer{}, handler.RouterOptions{}, nil) // valid token, zero permissions

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
		r := newTestRouterWithOptions(&bytes.Buffer{}, handler.RouterOptions{EnableSwagger: true})

		assert.Equal(t, http.StatusOK, send(r, http.MethodGet, "/swagger/index.html", nil).Code)

		spec := send(r, http.MethodGet, "/swagger/doc.json", nil)
		assert.Equal(t, http.StatusOK, spec.Code)
		assert.Contains(t, spec.Body.String(), "/api/v1/companies", "generated spec must include our routes")
		assert.Contains(t, spec.Body.String(), "BearerAuth", "spec must declare the bearer security scheme")
	})

	t.Run("disabled returns 404", func(t *testing.T) {
		r := newTestRouterWithOptions(&bytes.Buffer{}, handler.RouterOptions{EnableSwagger: false})

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
	sec := handler.RouterSecurity{Tokens: fakeTokens{}, Permissions: fakePermissions(nil), AuthLimiter: ratelimit.New(1, time.Minute)}
	r := handler.NewRouter(log, handler.RouterOptions{}, sec, handler.Handlers{
		Health:    handler.NewHealthHandler(okPinger{}, log),
		Auth:      handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
		User:      handler.NewUserHandler(nil),
		Driver:    handler.NewDriverHandler(nil),
		Truck:     handler.NewTruckHandler(nil),
		ELDDevice: handler.NewELDDeviceHandler(nil),
		Company:   handler.NewCompanyHandler(failingCompanyService{}),
		RateLimit: handler.NewRateLimitHandler(nil),
		Password:  handler.NewPasswordResetHandler(nil, nil),
	})

	// The body "{" is invalid, so the first call stops at 400 in the handler.
	assert.Equal(t, http.StatusBadRequest, send(r, http.MethodPost, "/api/v1/auth/login", nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, send(r, http.MethodPost, "/api/v1/auth/refresh", nil).Code,
		"one limit shared by all /auth routes")
	assert.Equal(t, http.StatusOK, send(r, http.MethodGet, "/health", nil).Code, "other routes are not limited")
}

func TestRouter_SecurityHeadersOnAPIButNotSwagger(t *testing.T) {
	r := newTestRouterWithOptions(&bytes.Buffer{}, handler.RouterOptions{EnableSwagger: true})

	api := send(r, http.MethodGet, "/api/v1/companies", authHeader)
	assert.Equal(t, "no-store", api.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", api.Header().Get("X-Content-Type-Options"))
	assert.Empty(t, api.Header().Get("Strict-Transport-Security"), "HSTS off unless enabled")

	login := send(r, http.MethodPost, "/api/v1/auth/login", nil)
	assert.Equal(t, "no-store", login.Header().Get("Cache-Control"), "token responses must never be cached")

	ui := send(r, http.MethodGet, "/swagger/index.html", nil)
	assert.Equal(t, http.StatusOK, ui.Code)
	assert.Empty(t, ui.Header().Get("Content-Security-Policy"), "the CSP would block Swagger UI's scripts")
}

func TestRouter_TrustedProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := logger.New(&bytes.Buffer{}, true, "debug")
	var seen string
	recordIP := recordingLimiter(func(key string) { seen = key })

	for _, tc := range []struct {
		name    string
		trusted []string
		want    string
	}{
		{"no proxy trusted: X-Forwarded-For is ignored", nil, "10.0.0.7"},
		{"load balancer trusted: the real client IP is used", []string{"10.0.0.0/8"}, "203.0.113.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := handler.RouterSecurity{Tokens: fakeTokens{}, Permissions: fakePermissions(nil), AuthLimiter: recordIP}
			r := handler.NewRouter(log, handler.RouterOptions{TrustedProxies: tc.trusted}, sec, handler.Handlers{
				Health:    handler.NewHealthHandler(okPinger{}, log),
				Auth:      handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
				User:      handler.NewUserHandler(nil),
				Driver:    handler.NewDriverHandler(nil),
				Truck:     handler.NewTruckHandler(nil),
				ELDDevice: handler.NewELDDeviceHandler(nil),
				Company:   handler.NewCompanyHandler(failingCompanyService{}),
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("{"))
			req.RemoteAddr = "10.0.0.7:4444" // the load balancer
			req.Header.Set("X-Forwarded-For", "203.0.113.50")

			r.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tc.want, seen)
		})
	}
}

// recordingLimiter allows everything and reports each key it sees.
type recordingLimiter func(key string)

func (f recordingLimiter) Take(_ context.Context, key string) (time.Duration, bool) {
	f(key)
	return 0, true
}

// Read access must not imply write access to security settings.
func TestRouter_RateLimitUpdateNeedsUpdatePermission(t *testing.T) {
	r := newTestRouterWith(&bytes.Buffer{}, handler.RouterOptions{}, []model.Permission{model.PermRateLimitRead})

	rec := send(r, http.MethodPatch, "/api/v1/rate-limits/login_attempts", authHeader)

	require.Equal(t, http.StatusForbidden, rec.Code)
	var body apierror.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, string(model.PermRateLimitUpdate), body.Error.Details["permission"])
}

// Editing a user must not imply setting their password.
func TestRouter_SetPasswordNeedsItsOwnPermission(t *testing.T) {
	r := newTestRouterWith(&bytes.Buffer{}, handler.RouterOptions{}, []model.Permission{model.PermUserUpdate})

	rec := send(r, http.MethodPut, "/api/v1/users/"+uuid.NewString()+"/password", authHeader)

	require.Equal(t, http.StatusForbidden, rec.Code)
	var body apierror.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, string(model.PermUserSetPassword), body.Error.Details["permission"])
}

func TestRouter_BillingRoutesOnlyWhenEnabled(t *testing.T) {
	// Billing on: an invalid UUID reaches the handler (400).
	on := newTestRouter(&bytes.Buffer{})
	assert.Equal(t, http.StatusBadRequest, send(on, http.MethodGet, "/api/v1/products/not-a-uuid", authHeader).Code)

	// Billing off (no Stripe keys): the routes do not exist.
	gin.SetMode(gin.TestMode)
	log := logger.New(&bytes.Buffer{}, true, "debug")
	sec := handler.RouterSecurity{Tokens: fakeTokens{}, Permissions: fakePermissions(model.AllPermissions()),
		AuthLimiter: ratelimit.New(1000, time.Minute)}
	off := handler.NewRouter(log, handler.RouterOptions{}, sec, handler.Handlers{
		Health:    handler.NewHealthHandler(okPinger{}, log),
		Auth:      handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
		User:      handler.NewUserHandler(nil),
		Driver:    handler.NewDriverHandler(nil),
		Truck:     handler.NewTruckHandler(nil),
		ELDDevice: handler.NewELDDeviceHandler(nil),
		Company:   handler.NewCompanyHandler(nil),
		RateLimit: handler.NewRateLimitHandler(nil),
		Password:  handler.NewPasswordResetHandler(nil, nil),
		AuditLog:  handler.NewAuditLogHandler(nil),
	})
	for _, path := range []string{"/api/v1/products", "/api/v1/products/" + uuid.NewString()} {
		assert.Equal(t, http.StatusNotFound, send(off, http.MethodGet, path, authHeader).Code, path)
	}
}

func TestRouter_Frontend(t *testing.T) {
	r := newTestRouter(&bytes.Buffer{})

	rec := send(r, http.MethodGet, "/", nil)
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/app/", rec.Header().Get("Location"))

	rec = send(r, http.MethodGet, "/app/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "js.stripe.com", "the page loads Stripe.js")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "https://js.stripe.com",
		"the page gets the frontend CSP, not the API's default-src 'none'")

	rec = send(r, http.MethodGet, "/app/app.js", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "javascript")

	assert.Equal(t, http.StatusNotFound, send(r, http.MethodGet, "/app/nope.js", nil).Code)
}

func TestRouter_PaymentMethodRoutesRegistered(t *testing.T) {
	r := newTestRouter(&bytes.Buffer{})

	// Invalid company UUID: 400 from the handler proves the route exists.
	rec := send(r, http.MethodGet, "/api/v1/companies/not-a-uuid/billing/payment-methods", authHeader)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// companyTokens: "company-token" belongs to a user of a company (the
// billing gate only applies to company users).
type companyTokens struct{}

func (companyTokens) Parse(token string) (*auth.TokenClaims, error) {
	if token != "company-token" {
		return nil, errors.New("invalid")
	}
	company := uuid.New()
	return &auth.TokenClaims{UserID: uuid.New(), CompanyID: &company}, nil
}

type blockedBilling struct{}

func (blockedBilling) BillingStatus(context.Context, uuid.UUID) (string, error) {
	return model.BillingNone, nil
}

func TestRouter_BillingGateOnlyOnTheProduct(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := logger.New(&bytes.Buffer{}, true, "debug")
	sec := handler.RouterSecurity{Tokens: companyTokens{}, Permissions: fakePermissions(model.AllPermissions()),
		AuthLimiter: ratelimit.New(1000, time.Minute), Billing: blockedBilling{}}
	r := handler.NewRouter(log, handler.RouterOptions{}, sec, handler.Handlers{
		Health: handler.NewHealthHandler(okPinger{}, log), Auth: handler.NewAuthHandler(nil, ratelimit.New(1000, time.Minute)),
		User: handler.NewUserHandler(nil), Driver: handler.NewDriverHandler(nil), Truck: handler.NewTruckHandler(nil),
		ELDDevice: handler.NewELDDeviceHandler(nil), Company: handler.NewCompanyHandler(nil),
		RateLimit: handler.NewRateLimitHandler(nil), Password: handler.NewPasswordResetHandler(nil, nil),
		AuditLog: handler.NewAuditLogHandler(nil), PaymentMethod: handler.NewPaymentMethodHandler(nil),
		Subscription: handler.NewSubscriptionHandler(nil),
	})
	h := map[string]string{"Authorization": "Bearer company-token"}

	// The fleet: 402 for a company without a paid-up subscription.
	for _, path := range []string{"/api/v1/trucks", "/api/v1/drivers", "/api/v1/drivers/me", "/api/v1/eld-devices/" + uuid.NewString()} {
		rec := send(r, http.MethodGet, path, h)
		assert.Equal(t, http.StatusPaymentRequired, rec.Code, path)
	}

	// Billing and the company itself stay reachable, or a blocked company
	// could never pay. (400 = the handler ran: the gate let it through.)
	for _, path := range []string{
		"/api/v1/companies/not-a-uuid",
		"/api/v1/companies/not-a-uuid/billing/payment-methods",
		"/api/v1/companies/not-a-uuid/billing/subscription",
	} {
		rec := send(r, http.MethodGet, path, h)
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}
