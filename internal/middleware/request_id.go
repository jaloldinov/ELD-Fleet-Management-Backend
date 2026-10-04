// Package middleware contains Gin middleware: code that runs before and/or
// after every handler (request IDs, logging, recovery, auth, permissions).
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/pkg/requestid"
)

const RequestIDHeader = "X-Request-ID"

// RequestID gives every request a unique ID.
//
// If the caller (e.g. a load balancer) already sent a safe X-Request-ID, we
// reuse it so one ID can be followed across several services. Otherwise we
// generate a new UUID. The ID is stored in the request context and echoed in
// the response header.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if !isSafeRequestID(id) {
			id = uuid.NewString()
		}

		c.Request = c.Request.WithContext(requestid.NewContext(c.Request.Context(), id))
		c.Header(RequestIDHeader, id)

		c.Next()
	}
}

// isSafeRequestID rejects empty, overly long, or unusual values. The ID ends up
// in our logs, so we never trust arbitrary client input there (log injection).
func isSafeRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlphaNum && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
