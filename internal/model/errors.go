// Package model contains the domain types shared by all layers.
//
// These structs describe *our* data. They have no json tags on purpose:
// request/response shapes (DTOs) live in the handler layer.
package model

import "errors"

// Domain errors. The repository returns them, the service may return them
// (or its own), and the handler maps them to HTTP status codes.
// Callers compare with errors.Is(err, model.ErrNotFound).
var (
	ErrNotFound = errors.New("not found")
	// ErrForbidden: the caller may not perform this action, decided by a
	// business rule in the service (permissions are checked earlier, by
	// middleware). Becomes HTTP 403.
	ErrForbidden       = errors.New("you are not allowed to perform this action")
	ErrDOTNumberTaken  = errors.New("dot number is already used by another company")
	ErrEmailTaken      = errors.New("email is already used by another user")
	ErrUsernameTaken   = errors.New("username is already used by another user")
	ErrRoleNotFound    = errors.New("role not found")
	ErrLicenseTaken    = errors.New("this license number is already registered in that state")
	ErrVINTaken        = errors.New("a truck with this VIN is already registered")
	ErrUnitNumberTaken = errors.New("this unit number is already used in your fleet")
	ErrSerialTaken     = errors.New("an ELD device with this serial number is already registered")
	ErrTruckHasELD     = errors.New("this truck already has an ELD device installed")

	// ErrInvalidCredentials is deliberately the same for "no such user" and
	// "wrong password", so attackers cannot learn which accounts exist.
	ErrInvalidCredentials = errors.New("invalid login or password")
	ErrUserInactive       = errors.New("account is disabled")

	// ErrRefreshTokenInvalid: unknown, expired or revoked refresh token.
	ErrRefreshTokenInvalid = errors.New("invalid or expired refresh token")
	// ErrRefreshTokenReused: an already-rotated token was presented again,
	// which means a copy was stolen. Clients see the same 401 as
	// ErrRefreshTokenInvalid; the distinct error exists so it gets logged.
	ErrRefreshTokenReused = errors.New("refresh token reuse detected")

	// Password reset. Deliberately vague: "no such email", "wrong code",
	// "expired" and "too many attempts" all look the same to the client.
	ErrResetCodeInvalid  = errors.New("invalid or expired code")
	ErrResetTokenInvalid = errors.New("invalid or expired reset token")

	// ErrPaymentProvider: Stripe refused a request or could not be reached.
	// Wrapped around the Stripe error (errors.Is still finds this one), so
	// the details are logged while clients get a short 502.
	ErrPaymentProvider = errors.New("payment provider error")
)
