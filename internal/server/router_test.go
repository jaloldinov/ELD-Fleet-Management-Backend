package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/internal/middleware"
)

// These tests check the wiring: that routes exist and the middleware chain is
// active. Each middleware's own behaviour is tested in the middleware package.

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

func newTestRouter(buf *bytes.Buffer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := logger.New(buf, true, "debug")
	return NewRouter(log, Handlers{
		Health: handler.NewHealthHandler(okPinger{}, log),
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
	assert.Contains(t, buf.String(), `"level":"WARN"`)
}
