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

func TestAppSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(AppSecurityHeaders(false))
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "script-src 'self' https://js.stripe.com", "our scripts and Stripe.js only")
	assert.Contains(t, csp, "frame-src https://js.stripe.com", "Stripe's card form runs in a frame")
	assert.Contains(t, csp, "connect-src 'self' https://api.stripe.com")
	assert.Contains(t, csp, "frame-ancestors 'none'", "nobody may frame our page (clickjacking)")
	assert.NotContains(t, csp, "unsafe-inline", "inline scripts would make XSS possible")
	assert.NotContains(t, csp, "unsafe-eval")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Empty(t, rec.Header().Get("Strict-Transport-Security"))
}
