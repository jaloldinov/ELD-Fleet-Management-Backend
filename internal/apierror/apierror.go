// Package apierror defines the API's JSON error format and writes it.
//
// It is its own small package because two packages write errors: handler
// (for service errors) and middleware (for auth, permissions, body size,
// rate limits). Handler imports middleware to build the router, so
// middleware cannot import handler (Go forbids import cycles). Both import
// this package instead.
package apierror

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// Stable, machine-readable error codes. Clients branch on these; the
// human-readable message may change.
const (
	CodeBadRequest   = "bad_request"
	CodeValidation   = "validation_failed"
	CodeUnauthorized = "unauthorized" // not logged in / bad credentials / bad token
	CodeForbidden    = "forbidden"    // logged in, but not allowed
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeTooLarge     = "payload_too_large"
	CodeRateLimited  = "rate_limited"
	CodeInternal     = "internal_error"
	// Stripe refused a request or could not be reached (HTTP 502).
	CodePaymentProvider = "payment_provider_error"

	// Password reset: "wrong or expired code, try again" vs "the reset
	// token is no longer valid, start over".
	CodeInvalidResetCode  = "invalid_reset_code"
	CodeInvalidResetToken = "invalid_reset_token"
)

// Response is the body of every error response in the API.
type Response struct {
	Error Body `json:"error"`
}

type Body struct {
	Code    string            `json:"code" example:"validation_failed"`
	Message string            `json:"message" example:"request validation failed"`
	Details map[string]string `json:"details,omitempty"`
}

// Respond writes an error in the standard API format and aborts the request.
func Respond(c *gin.Context, status int, code, message string, details map[string]string) {
	// Abort stops any remaining handlers in the chain from running.
	c.AbortWithStatusJSON(status, Response{
		Error: Body{Code: code, Message: message, Details: details},
	})
}

// RespondRateLimited writes a 429 with a Retry-After header (whole seconds,
// rounded up), so well-behaved clients know when to try again.
func RespondRateLimited(c *gin.Context, retryAfter time.Duration) {
	secs := int64(math.Ceil(retryAfter.Seconds()))
	c.Header("Retry-After", strconv.FormatInt(secs, 10))
	Respond(c, http.StatusTooManyRequests, CodeRateLimited, "too many requests, try again later", nil)
}
