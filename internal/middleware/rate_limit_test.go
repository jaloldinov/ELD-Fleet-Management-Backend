package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/ratelimit"
)

func TestRateLimitByIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitByIP(ratelimit.New(2, time.Minute)))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	call := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusOK, call("198.51.100.1").Code)
	assert.Equal(t, http.StatusOK, call("198.51.100.1").Code)
	blocked := call("198.51.100.1")
	assert.Equal(t, http.StatusTooManyRequests, blocked.Code)
	assert.Equal(t, "30", blocked.Header().Get("Retry-After"), "2 per minute: next token in 30s")
	assert.Contains(t, blocked.Body.String(), `"code":"rate_limited"`)

	assert.Equal(t, http.StatusOK, call("198.51.100.2").Code, "another IP has its own limit")
}
