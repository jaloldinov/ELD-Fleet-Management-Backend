package handler

import (
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/service"
)

// UserResponse is a user as returned by the API. It has no password field of
// any kind, by design: model.User.PasswordHash must never leave the server.
type UserResponse struct {
	ID uuid.UUID `json:"id" swaggertype:"string" format:"uuid" example:"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"`
	// Null for platform users.
	CompanyID   *uuid.UUID `json:"company_id" swaggertype:"string" format:"uuid" example:"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"`
	Email       *string    `json:"email" example:"jane@acme.com"`
	Username    *string    `json:"username" example:"jane.smith"`
	FirstName   string     `json:"first_name" example:"Jane"`
	LastName    string     `json:"last_name" example:"Smith"`
	IsActive    bool       `json:"is_active" example:"true"`
	LastLoginAt *time.Time `json:"last_login_at" example:"2026-10-02T11:26:26Z"`
	CreatedAt   time.Time  `json:"created_at" example:"2026-10-02T11:26:26Z"`
}

// MeResponse is the current user plus what they are allowed to do. Clients use
// permissions to show/hide UI; the server still enforces them on every request.
type MeResponse struct {
	UserResponse
	Permissions []string `json:"permissions" example:"truck:read,driver:read"`
}

func newMeResponse(p *service.UserProfile) MeResponse {
	u := p.User
	var lastLogin *time.Time
	if u.LastLoginAt != nil {
		t := u.LastLoginAt.UTC()
		lastLogin = &t
	}

	perms := make([]string, 0, len(p.Permissions)) // [] not null when empty
	for _, perm := range p.Permissions {
		perms = append(perms, string(perm))
	}

	return MeResponse{
		UserResponse: UserResponse{
			ID: u.ID, CompanyID: u.CompanyID, Email: u.Email, Username: u.Username,
			FirstName: u.FirstName, LastName: u.LastName, IsActive: u.IsActive,
			LastLoginAt: lastLogin, CreatedAt: u.CreatedAt.UTC(),
		},
		Permissions: perms,
	}
}
