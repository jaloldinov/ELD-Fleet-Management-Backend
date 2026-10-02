package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/logger"
)

// fakePinger is a hand-written test double: it returns whatever error we set.
type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error { return f.err }

func serve(h gin.HandlerFunc, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(path, h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthHandler_Live(t *testing.T) {
	// Liveness must be OK even when the database is down.
	h := NewHealthHandler(fakePinger{err: errors.New("db down")}, logger.New(&bytes.Buffer{}, true, "info"))

	rec := serve(h.Live, "/health")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestHealthHandler_Ready(t *testing.T) {
	tests := []struct {
		name     string
		pingErr  error
		wantCode int
		wantBody string
		wantLog  bool
	}{
		{
			name:     "database up",
			pingErr:  nil,
			wantCode: http.StatusOK,
			wantBody: `{"status":"ok","database":"up"}`,
		},
		{
			name:     "database down",
			pingErr:  errors.New("connection refused"),
			wantCode: http.StatusServiceUnavailable,
			wantBody: `{"status":"unavailable","database":"down"}`,
			wantLog:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			h := NewHealthHandler(fakePinger{err: tt.pingErr}, logger.New(&logBuf, true, "info"))

			rec := serve(h.Ready, "/ready")

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "connection refused", "internal errors must not leak")
			if tt.wantLog {
				assert.Contains(t, logBuf.String(), "connection refused")
			} else {
				assert.Empty(t, logBuf.String())
			}
		})
	}
}
