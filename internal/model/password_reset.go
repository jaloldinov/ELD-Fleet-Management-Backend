package model

import (
	"time"

	"github.com/google/uuid"
)

// PasswordReset is one "forgot password" attempt of a user.
type PasswordReset struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	CodeHash      []byte // HMAC of the emailed code
	Attempts      int
	CodeExpiresAt time.Time
	CreatedAt     time.Time
}
