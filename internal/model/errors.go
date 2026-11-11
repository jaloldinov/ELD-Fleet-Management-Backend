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

	// Device API. ErrDeviceKeyInvalid is deliberately the same for "no such
	// key", "revoked" and "rotated away": the caller learns nothing.
	ErrDeviceKeyInvalid = errors.New("invalid device key")
	ErrDeviceRetired    = errors.New("this device is retired")

	// Driver ↔ truck assignments. Both come from partial unique indexes, so
	// they also hold when two requests arrive at the same moment.
	ErrDriverOnTruck = errors.New("this driver is already assigned to a truck")
	ErrSeatTaken     = errors.New("this truck already has a driver in that role")

	// Driver logbook. ErrDutyStatusTaken comes from a unique index, so it
	// also holds when two requests arrive at the same moment.
	ErrDutyStatusTaken = errors.New("the driver already has a duty status starting at that moment")
	// ErrTruckMoving: the driver is at the wheel of a moving truck; the ELD
	// has them driving until it stops.
	ErrTruckMoving = errors.New("the truck is moving: the duty status can be changed once it stops")
	// Edits.
	ErrRecordReplaced     = errors.New("this record was already changed: edit its newest version")
	ErrDrivingNotEditable = errors.New("driving is recorded by the ELD and cannot be edited")
	ErrEditChangesDriving = errors.New("this edit would change when the driver was driving, which only the ELD records")

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

	// ErrSubscriptionExists: the company already has a subscription that
	// has not ended. Change it instead of starting another one.
	ErrSubscriptionExists = errors.New("the company already has a subscription")

	// ErrCardDeclined: the bank refused a payment. Wrapped with the bank's
	// message, which is written for the card holder. Becomes HTTP 402.
	ErrCardDeclined = errors.New("the card was declined")

	// ErrAuthenticationRequired: the bank wants the card holder to confirm
	// the payment (3-D Secure); it can only be finished in the browser.
	ErrAuthenticationRequired = errors.New("the bank asks the card holder to confirm this payment")

	// ErrNoPaymentMethod: a payment was asked for, but the company has no
	// default card. The customer must fix it (add a card); retrying does not
	// help, so it is a 402, not a provider failure (502). Checked BEFORE
	// calling Stripe: our own rule, instead of guessing Stripe's error code.
	ErrNoPaymentMethod = errors.New("the company has no default card: add one on the billing page first")

	// ErrBillingInactive: the company's subscription does not allow using
	// the product (not paid yet, or Stripe gave up). Becomes HTTP 402.
	ErrBillingInactive = errors.New("the company's subscription is not active")

	// ErrWebhookSignature: a webhook was not signed by Stripe (or is too old
	// to be trusted). Becomes HTTP 400, so Stripe would not retry it.
	ErrWebhookSignature = errors.New("invalid webhook signature")
)
