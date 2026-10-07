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

// appCSP allows exactly what the frontend at /app/ needs, nothing more:
//   - our own files ('self'), and Stripe.js from js.stripe.com;
//   - Stripe's card form and 3-D Secure, which run in frames from Stripe;
//   - fetch() to our API ('self') and to Stripe's API (Stripe.js calls it).
//
// No 'unsafe-inline': an attacker who sneaks <script>...</script> into the
// page (XSS) gets it refused by the browser, and cannot steal the token.
const appCSP = "default-src 'self'; " +
	"script-src 'self' https://js.stripe.com https://*.js.stripe.com; " +
	"frame-src https://js.stripe.com https://*.js.stripe.com https://hooks.stripe.com; " +
	"connect-src 'self' https://api.stripe.com; " +
	"img-src 'self' data: https://*.stripe.com; " +
	"style-src 'self'; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// AppSecurityHeaders is SecurityHeaders for the HTML frontend: the same
// protections, with a CSP that lets the page (and Stripe.js) run.
func AppSecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		// The files are embedded in the binary: they change only with a new
		// release, but "no-cache" makes the browser check, so a deploy is
		// picked up at once (an unchanged file still answers 304).
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", appCSP)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		c.Next()
	}
}
