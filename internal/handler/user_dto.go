package handler

import (
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type CreateUserRequest struct {
	// Platform users only: the company the new user belongs to (omit for a
	// platform user). Company users may omit it; their own company is used.
	CompanyID *uuid.UUID `json:"company_id,omitempty" swaggertype:"string" format:"uuid" example:"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"`
	// Email and/or username; at least one is required.
	Email    string `json:"email,omitempty" example:"jane@acme.com"`
	Username string `json:"username,omitempty" example:"jane.smith"`
	// 8 characters to 72 bytes.
	Password  string   `json:"password" example:"correct horse battery staple"`
	FirstName string   `json:"first_name" example:"Jane"`
	LastName  string   `json:"last_name" example:"Smith"`
	Roles     []string `json:"roles" example:"dispatcher"`
}

func (r CreateUserRequest) toInput() service.CreateUserInput {
	return service.CreateUserInput{
		CompanyID: r.CompanyID, Email: r.Email, Username: r.Username, Password: r.Password,
		FirstName: r.FirstName, LastName: r.LastName, Roles: r.Roles,
	}
}

// UpdateUserRequest is a partial update: omitted fields are unchanged.
// roles, when present, REPLACES all of the user's roles.
type UpdateUserRequest struct {
	FirstName *string   `json:"first_name,omitempty" example:"Jane"`
	LastName  *string   `json:"last_name,omitempty" example:"Smith"`
	IsActive  *bool     `json:"is_active,omitempty" example:"false"`
	Roles     *[]string `json:"roles,omitempty" example:"dispatcher,manager"`
}

func (r UpdateUserRequest) toInput() service.UpdateUserInput {
	return service.UpdateUserInput{
		FirstName: r.FirstName, LastName: r.LastName, IsActive: r.IsActive, Roles: r.Roles,
	}
}

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
	Roles       []string   `json:"roles" example:"dispatcher"`
	LastLoginAt *time.Time `json:"last_login_at" example:"2026-10-02T11:26:26Z"`
	CreatedAt   time.Time  `json:"created_at" example:"2026-10-02T11:26:26Z"`
}

func newUserResponse(u *model.User, roles []string) UserResponse {
	var lastLogin *time.Time
	if u.LastLoginAt != nil {
		t := u.LastLoginAt.UTC()
		lastLogin = &t
	}
	if roles == nil {
		roles = []string{} // [] not null
	}
	return UserResponse{
		ID: u.ID, CompanyID: u.CompanyID, Email: u.Email, Username: u.Username,
		FirstName: u.FirstName, LastName: u.LastName, IsActive: u.IsActive, Roles: roles,
		LastLoginAt: lastLogin, CreatedAt: u.CreatedAt.UTC(),
	}
}

type ListUsersResponse struct {
	Items    []UserResponse `json:"items"`
	Page     int            `json:"page" example:"1"`
	PageSize int            `json:"page_size" example:"20"`
	Total    int            `json:"total" example:"12"`
}

func newListUsersResponse(p *service.UserPage) ListUsersResponse {
	items := make([]UserResponse, 0, len(p.Items))
	for _, it := range p.Items {
		items = append(items, newUserResponse(it.User, it.Roles))
	}
	return ListUsersResponse{Items: items, Page: p.Page, PageSize: p.PageSize, Total: p.Total}
}

// MeResponse is the current user plus what they are allowed to do. Clients use
// permissions to show/hide UI; the server still enforces them on every request.
type MeResponse struct {
	UserResponse
	Permissions []string `json:"permissions" example:"truck:read,driver:read"`
}

func newMeResponse(p *service.UserProfile) MeResponse {
	perms := make([]string, 0, len(p.Permissions)) // [] not null when empty
	for _, perm := range p.Permissions {
		perms = append(perms, string(perm))
	}
	return MeResponse{UserResponse: newUserResponse(p.User, p.Roles), Permissions: perms}
}

type SetPasswordRequest struct {
	// At least 8 characters, at most 72 bytes.
	NewPassword string `json:"new_password" example:"correct horse battery staple"`
}
