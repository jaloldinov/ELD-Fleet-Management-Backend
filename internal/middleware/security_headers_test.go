package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSecurityHeaders(t *testing.T) {
	for _, hsts := range []bool{false, true} {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.Use(SecurityHeaders(hsts))
		r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		h := rec.Header()
		assert.Equal(t, "no-store", h.Get("Cache-Control"))
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
		assert.Contains(t, h.Get("Content-Security-Policy"), "default-src 'none'")
		assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
		assert.Equal(t, hsts, h.Get("Strict-Transport-Security") != "", "HSTS only when enabled")
	}
}
