package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/requestid"
)

// Recovery catches a panic in any later middleware or handler, logs it with a
// stack trace, and returns 500. Without it, one bad request could crash the
// whole server.
//
// The client only sees a generic message: stack traces and internal error
// details must never be sent in a response.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(c.Request.Context(), "panic recovered",
					"request_id", requestid.FromContext(c.Request.Context()),
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
			}
		}()

		c.Next()
	}
}
