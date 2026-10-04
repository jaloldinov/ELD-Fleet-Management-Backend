package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/logger"
	"github.com/jaloldinov/ELD-learning/pkg/requestid"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// doRequest sends a request through r in memory and returns the recorded response.
func doRequest(r http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// decodeLogLines parses JSON log output (one JSON object per line).
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		lines = append(lines, entry)
	}
	return lines
}

// ---------- RequestID ----------

func TestRequestID(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		wantSame bool // true: incoming ID must be reused
	}{
		{name: "no header generates uuid", incoming: "", wantSame: false},
		{name: "safe header is reused", incoming: "lb-abc_123", wantSame: true},
		{name: "unsafe header is replaced", incoming: "bad id\nfake log line", wantSame: false},
		{name: "too long header is replaced", incoming: strings.Repeat("a", 65), wantSame: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var idInHandler string
			r := gin.New()
			r.Use(RequestID())
			r.GET("/", func(c *gin.Context) {
				idInHandler = requestid.FromContext(c.Request.Context())
			})

			headers := map[string]string{}
			if tt.incoming != "" {
				headers[RequestIDHeader] = tt.incoming
			}
			rec := doRequest(r, http.MethodGet, "/", headers)

			idInHeader := rec.Header().Get(RequestIDHeader)
			assert.Equal(t, idInHeader, idInHandler, "handler and response must see the same ID")

			if tt.wantSame {
				assert.Equal(t, tt.incoming, idInHeader)
			} else {
				_, err := uuid.Parse(idInHeader)
				assert.NoError(t, err, "a new UUID must be generated")
			}
		})
	}
}

// ---------- RequestLogger ----------

func TestRequestLogger_LevelDependsOnStatus(t *testing.T) {
	tests := []struct {
		status    int
		wantLevel string
	}{
		{status: http.StatusOK, wantLevel: "INFO"},
		{status: http.StatusNotFound, wantLevel: "WARN"},
		{status: http.StatusInternalServerError, wantLevel: "ERROR"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var buf bytes.Buffer
			r := gin.New()
			r.Use(RequestID(), RequestLogger(logger.New(&buf, true, "debug")))
			r.GET("/trucks/:id", func(c *gin.Context) { c.Status(tt.status) })

			rec := doRequest(r, http.MethodGet, "/trucks/42", nil)

			lines := decodeLogLines(t, &buf)
			require.Len(t, lines, 1)
			entry := lines[0]
			assert.Equal(t, tt.wantLevel, entry["level"])
			assert.Equal(t, "http request", entry["msg"])
			assert.Equal(t, "GET", entry["method"])
			assert.Equal(t, "/trucks/42", entry["path"])
			assert.Equal(t, "/trucks/:id", entry["route"])
			assert.EqualValues(t, tt.status, entry["status"])
			assert.Equal(t, rec.Header().Get(RequestIDHeader), entry["request_id"])
		})
	}
}

// ---------- Recovery ----------

func TestRecovery_PanicReturns500AndLogs(t *testing.T) {
	var buf bytes.Buffer
	r := gin.New()
	r.Use(Recovery(logger.New(&buf, true, "debug")))
	r.GET("/boom", func(c *gin.Context) { panic("something went wrong") })

	rec := doRequest(r, http.MethodGet, "/boom", nil)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"internal server error"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "something went wrong", "panic details must not leak to the client")

	lines := decodeLogLines(t, &buf)
	require.Len(t, lines, 1)
	assert.Equal(t, "panic recovered", lines[0]["msg"])
	assert.Equal(t, "something went wrong", lines[0]["panic"])
	assert.NotEmpty(t, lines[0]["stack"])
}

func TestRecovery_NoPanicPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	r := gin.New()
	r.Use(Recovery(logger.New(&buf, true, "debug")))
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	rec := doRequest(r, http.MethodGet, "/ok", nil)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, buf.String())
}
