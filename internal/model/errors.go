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
	ErrNotFound       = errors.New("not found")
	ErrDOTNumberTaken = errors.New("dot number is already used by another company")
)
