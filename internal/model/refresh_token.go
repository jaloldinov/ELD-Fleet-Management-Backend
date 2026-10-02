package model

import (
	"time"

	"github.com/google/uuid"
)

// RefreshToken is a stored (hashed) refresh token: one per login session,
// replaced by a new one on every refresh.
type RefreshToken struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	TokenHash    []byte // SHA-256 of the token; the plain token is never stored
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	ReplacedByID *uuid.UUID
	UserAgent    string
	IP           string
	CreatedAt    time.Time
}

// ClientInfo describes the device/app a session was created from.
type ClientInfo struct {
	UserAgent string
	IP        string
}
