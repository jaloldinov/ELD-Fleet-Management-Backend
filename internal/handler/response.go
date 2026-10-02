// Package handler is the HTTP layer. Handlers translate between HTTP and the
// service layer: parse the request, call the service, write the response.
// They contain no business rules.
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Stable, machine-readable error codes. Clients branch on these; the
// human-readable message may change.
const (
	CodeBadRequest = "bad_request"
	CodeValidation = "validation_failed"
	CodeNotFound   = "not_found"
	CodeConflict   = "conflict"
	CodeInternal   = "internal_error"
)

// ErrorResponse is the body of every error response in the API.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string            `json:"code" example:"validation_failed"`
	Message string            `json:"message" example:"request validation failed"`
	Details map[string]string `json:"details,omitempty"`
}

func respondError(c *gin.Context, status int, code, message string, details map[string]string) {
	// Abort stops any remaining handlers in the chain from running.
	c.AbortWithStatusJSON(status, ErrorResponse{
		Error: ErrorBody{Code: code, Message: message, Details: details},
	})
}

// handleError is the single place where service/domain errors become HTTP
// responses. Every handler calls it, so the mapping is consistent everywhere.
func handleError(c *gin.Context, err error) {
	var vErr *model.ValidationError

	switch {
	case errors.As(err, &vErr):
		respondError(c, http.StatusBadRequest, CodeValidation, "request validation failed", vErr.Fields)
	case errors.Is(err, model.ErrNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, "resource not found", nil)
	case errors.Is(err, model.ErrDOTNumberTaken):
		respondError(c, http.StatusConflict, CodeConflict, err.Error(), nil)
	default:
		// Unexpected error: a bug or an outage. Attach it to the context so the
		// RequestLogger middleware logs it, and hide the details from the client.
		_ = c.Error(err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "internal server error", nil)
	}
}

// NotFound answers requests to routes that do not exist, in our error format
// (Gin's default is a plain-text "404 page not found").
func NotFound(c *gin.Context) {
	respondError(c, http.StatusNotFound, CodeNotFound, "route not found", nil)
}

// bindJSON decodes the request body into dst. On failure it writes a 400 and
// returns false. The decoder's message is not returned to the client because
// it can mention Go type names.
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		respondError(c, http.StatusBadRequest, CodeBadRequest, "request body must be valid JSON", nil)
		return false
	}
	return true
}

// parseUUIDParam reads a UUID path parameter. On failure it writes a 400 and
// returns false.
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "request validation failed",
			map[string]string{name: "must be a valid UUID"})
		return uuid.Nil, false
	}
	return id, true
}

// queryInt reads an optional integer query parameter. A missing parameter
// returns 0 (meaning "use the default"); ok is false only for non-integers.
func queryInt(c *gin.Context, key string) (n int, ok bool) {
	s := c.Query(key)
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
