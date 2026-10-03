package model

import (
	"time"

	"github.com/google/uuid"
)

// Rate-limit rule names (rate_limits.name). Each rule is configured
// independently.
const (
	// RateLimitAuthRequests: requests per client IP to the public /auth routes.
	RateLimitAuthRequests = "auth_requests"
	// RateLimitLoginAttempts: login attempts per login name + client IP.
	RateLimitLoginAttempts = "login_attempts"
	// RateLimitPasswordReset: "send me a reset code" requests per email.
	RateLimitPasswordReset = "password_reset_requests"
)

// RateLimit allows at most Limit events per Window for each key (a client
// IP, a login...). A platform setting, changed at runtime through the API.
type RateLimit struct {
	Name        string
	Limit       int
	Window      time.Duration // whole seconds
	Description string
	UpdatedAt   time.Time
	UpdatedBy   *uuid.UUID // nil = never changed since the seed
}
