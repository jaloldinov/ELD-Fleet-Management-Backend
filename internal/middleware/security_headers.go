package middleware

import "github.com/gin-gonic/gin"

// SecurityHeaders sets response headers that make browsers and proxies
// handle our JSON responses safely. Meant for API routes only: the CSP
// below would block the scripts of an HTML page such as Swagger UI.
func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		// Responses contain tokens and personal data: never store them in a
		// browser or proxy cache.
		h.Set("Cache-Control", "no-store")
		// Do not guess a different content type than the one we send.
		h.Set("X-Content-Type-Options", "nosniff")
		// A JSON API loads nothing and is never shown in a frame.
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY") // older browsers' frame-ancestors
		h.Set("Referrer-Policy", "no-referrer")
		if hsts {
			// Browsers use HTTPS only for this host for the next 2 years.
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		c.Next()
	}
}
