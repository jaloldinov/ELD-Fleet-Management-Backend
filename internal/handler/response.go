// Package handler is the HTTP layer. Handlers translate between HTTP and the
// service layer: parse the request, call the service, write the response.
// They contain no business rules.
package handler

import (
	"encoding"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// handleError is the single place where service/domain errors become HTTP
// responses. Every handler calls it, so the mapping is consistent everywhere.
func handleError(c *gin.Context, err error) {
	var vErr *validation.ValidationError

	switch {
	case errors.As(err, &vErr):
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeValidation, "request validation failed", vErr.Fields)
	case errors.Is(err, model.ErrNotFound):
		apierror.Respond(c, http.StatusNotFound, apierror.CodeNotFound, "resource not found", nil)
	case errors.Is(err, model.ErrDOTNumberTaken),
		errors.Is(err, model.ErrEmailTaken),
		errors.Is(err, model.ErrUsernameTaken),
		errors.Is(err, model.ErrLicenseTaken),
		errors.Is(err, model.ErrVINTaken),
		errors.Is(err, model.ErrUnitNumberTaken),
		errors.Is(err, model.ErrSerialTaken),
		errors.Is(err, model.ErrTruckHasELD),
		errors.Is(err, model.ErrDriverOnTruck),
		errors.Is(err, model.ErrSeatTaken),
		errors.Is(err, model.ErrDutyStatusTaken),
		errors.Is(err, model.ErrTruckMoving),
		errors.Is(err, model.ErrRecordReplaced),
		errors.Is(err, model.ErrDrivingNotEditable),
		errors.Is(err, model.ErrEditChangesDriving),
		errors.Is(err, model.ErrSubscriptionExists):
		apierror.Respond(c, http.StatusConflict, apierror.CodeConflict, err.Error(), nil)
	case errors.Is(err, model.ErrInvalidCredentials):
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, err.Error(), nil)
	case errors.Is(err, model.ErrRefreshTokenReused):
		// Security event: attach it so the request log records it, but the
		// client gets exactly the same answer as for any invalid token.
		_ = c.Error(err)
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, model.ErrRefreshTokenInvalid.Error(), nil)
	case errors.Is(err, model.ErrRefreshTokenInvalid):
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, err.Error(), nil)
	case errors.Is(err, model.ErrResetCodeInvalid):
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeInvalidResetCode, err.Error(), nil)
	case errors.Is(err, model.ErrResetTokenInvalid):
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeInvalidResetToken, err.Error(), nil)
	case errors.Is(err, model.ErrForbidden):
		apierror.Respond(c, http.StatusForbidden, apierror.CodeForbidden, err.Error(), nil)
	case errors.Is(err, model.ErrUserInactive):
		apierror.Respond(c, http.StatusForbidden, apierror.CodeForbidden, err.Error(), nil)
	case errors.Is(err, model.ErrCardDeclined):
		// The bank's own words ("Your card has insufficient funds."), meant
		// for the person paying; nothing internal.
		msg := strings.TrimPrefix(err.Error(), model.ErrCardDeclined.Error()+": ")
		apierror.Respond(c, http.StatusPaymentRequired, apierror.CodeCardDeclined, msg, nil)
	case errors.Is(err, model.ErrAuthenticationRequired):
		apierror.Respond(c, http.StatusPaymentRequired, apierror.CodeAuthenticationRequired,
			"the bank asks the card holder to confirm this payment; pay it from the billing page", nil)
	case errors.Is(err, model.ErrNoPaymentMethod):
		apierror.Respond(c, http.StatusPaymentRequired, apierror.CodeNoPaymentMethod, err.Error(), nil)
	case errors.Is(err, model.ErrPaymentProvider):
		// 502 Bad Gateway: "a server we depend on failed", not our bug. The
		// Stripe error is logged; the client gets no Stripe internals.
		_ = c.Error(err)
		apierror.Respond(c, http.StatusBadGateway, apierror.CodePaymentProvider,
			"the payment provider could not complete the request; try again later", nil)
	default:
		// Unexpected error: a bug or an outage. Attach it to the context so the
		// RequestLogger middleware logs it, and hide the details from the client.
		_ = c.Error(err)
		apierror.Respond(c, http.StatusInternalServerError, apierror.CodeInternal, "internal server error", nil)
	}
}

// NotFound answers requests to routes that do not exist, in our error format
// (Gin's default is a plain-text "404 page not found").
func NotFound(c *gin.Context) {
	apierror.Respond(c, http.StatusNotFound, apierror.CodeNotFound, "route not found", nil)
}

// bindJSON decodes the request body into dst, strictly:
//   - unknown fields are rejected, so a typo like "unit_numbr" is an error
//     instead of a silently ignored field (and a 200 that changed nothing);
//   - the body must be exactly one JSON value;
//   - errors say what is wrong and, where possible, which field.
//
// On failure it writes the error response and returns false.
func bindJSON(c *gin.Context, dst any) bool {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errTrailingData
	}
	if err == nil {
		return true
	}

	var (
		tooLarge *http.MaxBytesError
		typeErr  *json.UnmarshalTypeError
		syntax   *json.SyntaxError
	)
	switch {
	case errors.As(err, &tooLarge):
		apierror.Respond(c, http.StatusRequestEntityTooLarge, apierror.CodeTooLarge,
			"request body must be at most "+strconv.FormatInt(tooLarge.Limit, 10)+" bytes", nil)
	case errors.Is(err, io.EOF):
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeBadRequest, "request body must not be empty", nil)
	case errors.As(err, &typeErr) && typeErr.Field != "":
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeValidation, "request validation failed",
			map[string]string{typeErr.Field: "must be " + jsonTypeName(typeErr.Type)})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// encoding/json has no typed error for this; the message format is
		// stable (and covered by a test).
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeValidation, "request validation failed",
			map[string]string{field: "unknown field"})
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, errTrailingData):
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeBadRequest, "request body must be valid JSON", nil)
	default:
		// A value the target type rejects, e.g. an invalid UUID string.
		// These messages describe the client's input, so they are safe to show.
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeBadRequest, "invalid request body: "+err.Error(), nil)
	}
	return false
}

var (
	errTrailingData     = errors.New("request body must contain a single JSON value")
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// jsonTypeName describes a Go type in JSON terms for error messages.
func jsonTypeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// uuid.UUID is a [16]byte, but in JSON it is a string.
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return "a string"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	default:
		return "an object"
	}
}

// parseUUIDParam reads a UUID path parameter. On failure it writes a 400 and
// returns false.
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		apierror.Respond(c, http.StatusBadRequest, apierror.CodeValidation, "request validation failed",
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

// queryUUID reads an optional UUID query parameter. A missing parameter
// returns nil; ok is false only for invalid UUIDs.
func queryUUID(c *gin.Context, key string) (id *uuid.UUID, ok bool) {
	s := c.Query(key)
	if s == "" {
		return nil, true
	}
	parsed, err := uuid.Parse(s)
	if err != nil {
		return nil, false
	}
	return &parsed, true
}
