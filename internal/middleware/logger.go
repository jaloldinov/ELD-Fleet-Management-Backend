package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/pkg/requestid"
)

// RequestLogger writes one structured log line per HTTP request.
//
// Code before c.Next() runs on the way IN; code after c.Next() runs on the way
// OUT, when the handler has finished and the status code is known.
//
// We deliberately do NOT log request bodies or query strings: they can contain
// passwords, tokens, or personal data.
func RequestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		status := c.Writer.Status()
		attrs := []any{
			"request_id", requestid.FromContext(c.Request.Context()),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"route", c.FullPath(), // route template, e.g. /trucks/:id (empty if no route matched)
			"status", status,
			"latency_ms", float64(time.Since(start).Microseconds()) / 1000,
			"client_ip", c.ClientIP(),
		}
		// Authenticate (deeper in the chain) has run by now, so for protected
		// routes we know who made the request.
		if p, ok := auth.PrincipalFromContext(c.Request.Context()); ok {
			attrs = append(attrs, "user_id", p.UserID.String())
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, "errors", c.Errors.String())
		}

		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError // our bug or outage: needs attention
		case status >= 400:
			level = slog.LevelWarn // client mistake: useful, not alarming
		}

		log.Log(c.Request.Context(), level, "http request", attrs...)
	}
}
