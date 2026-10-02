package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
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

func (failingCompanyService) List(context.Context, service.ListParams) (*service.CompanyPage, error) {
	return nil, errors.New("connection reset: secret internal detail")
}

func newTestRouter(buf *bytes.Buffer) *gin.Engine {
	return newTestRouterWithOptions(buf, Options{})
}

func newTestRouterWithOptions(buf *bytes.Buffer, opts Options) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := logger.New(buf, true, "debug")
	return NewRouter(log, opts, Handlers{
		Health:  handler.NewHealthHandler(okPinger{}, log),
		Company: handler.NewCompanyHandler(failingCompanyService{}),
	})
}

func TestRouter_ReadyRouteRegistered(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouter_HealthGoesThroughMiddleware(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

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

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"not_found","message":"route not found"}}`, rec.Body.String())
	assert.Contains(t, buf.String(), `"level":"WARN"`)
}

func TestRouter_CompanyRoutesRegistered(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	// An invalid UUID is rejected by the handler before the service is called,
	// so a 400 (not 404) proves the route exists.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/companies/not-a-uuid", nil))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRouter_InternalErrorIsLoggedButHiddenFromClient(t *testing.T) {
	var buf bytes.Buffer
	r := newTestRouter(&buf)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/companies", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret internal detail", "client must not see the cause")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "ERROR", entry["level"])
	assert.Contains(t, entry["errors"], "secret internal detail", "the log must contain the cause")
}

func TestRouter_Swagger(t *testing.T) {
	get := func(r http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	t.Run("enabled serves UI and spec", func(t *testing.T) {
		r := newTestRouterWithOptions(&bytes.Buffer{}, Options{EnableSwagger: true})

		assert.Equal(t, http.StatusOK, get(r, "/swagger/index.html").Code)

		spec := get(r, "/swagger/doc.json")
		assert.Equal(t, http.StatusOK, spec.Code)
		assert.Contains(t, spec.Body.String(), "/api/v1/companies", "generated spec must include our routes")
	})

	t.Run("disabled returns 404", func(t *testing.T) {
		r := newTestRouterWithOptions(&bytes.Buffer{}, Options{EnableSwagger: false})

		assert.Equal(t, http.StatusNotFound, get(r, "/swagger/index.html").Code)
	})
}
