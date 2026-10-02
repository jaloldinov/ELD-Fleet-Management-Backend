package model

import (
	"time"

	"github.com/google/uuid"
)

// User is anyone who can log in: platform staff, company staff, or a driver.
type User struct {
	ID        uuid.UUID
	CompanyID *uuid.UUID // nil = platform user
	Email     *string    // nil when the user logs in with a username only
	Username  *string    // nil when the user logs in with an email only
	// PasswordHash is a bcrypt hash. It must never be put into a response DTO
	// or a log line.
	PasswordHash string
	FirstName    string
	LastName     string
	IsActive     bool
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time
}

// IsPlatformUser reports whether the user works for the platform rather than
// for a company.
func (u *User) IsPlatformUser() bool {
	return u.CompanyID == nil
}
