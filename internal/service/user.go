package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

const (
	passwordMinLen = 8 // characters (NIST SP 800-63B minimum)
	userNameMaxLen = 100
)

// Same rule as the users_username_format CHECK constraint (after lowercasing).
var usernameRe = regexp.MustCompile(`^[a-z0-9._-]{3,50}$`)

// Which system roles each kind of user may have. The database cannot enforce
// this (it spans users.company_id and roles.scope), so the service does.
var (
	platformRoles = model.SystemRolesByScope(model.RoleScopePlatform)
	companyRoles  = model.SystemRolesByScope(model.RoleScopeCompany)
)

// UserRepository is what UserService needs from the data layer.
type UserRepository interface {
	Create(ctx context.Context, u model.User, roleNames []string) (*model.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	List(ctx context.Context, f model.UserFilter, limit, offset int) ([]model.User, int, error)
	Update(ctx context.Context, u model.User, roleNames []string) (*model.User, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	GetRoles(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID][]string, error)
	GetPermissions(ctx context.Context, userID uuid.UUID) ([]model.Permission, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error
}

// CompanyLookup checks that a company exists (repository.CompanyRepository).
type CompanyLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Company, error)
}

// SessionRevoker ends all sessions of a user (repository.SessionRepository).
type SessionRevoker interface {
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
}

// PasswordHasher hashes passwords. auth.BcryptHasher in production; tests use
// a fast fake, because real bcrypt is slow on purpose.
type PasswordHasher interface {
	Hash(password string) (string, error)
}

// UserDetails is a user together with their role names.
type UserDetails struct {
	User  *model.User
	Roles []string
}

// UserProfile is a user together with their roles and effective permissions.
type UserProfile struct {
	User        *model.User
	Roles       []string
	Permissions []model.Permission
}

type UserPage struct {
	Items    []UserDetails
	Total    int
	Page     int
	PageSize int
}

type CreateUserInput struct {
	// nil = platform user. Company users may only create users in their own
	// company: nil or their own id.
	CompanyID *uuid.UUID
	Email     string // email and/or username; at least one
	Username  string
	Password  string
	FirstName string
	LastName  string
	Roles     []string // system role names, e.g. model.RoleDispatcher
}

// UpdateUserInput is a partial update: nil fields are left unchanged.
// Roles, when set, REPLACES the user's roles.
type UpdateUserInput struct {
	FirstName *string
	LastName  *string
	IsActive  *bool
	Roles     *[]string
}

type UserListParams struct {
	CompanyID *uuid.UUID // platform users only; ignored for company users
	Page      int
	PageSize  int
}

type UserService struct {
	repo      UserRepository
	companies CompanyLookup
	sessions  SessionRevoker
	hasher    PasswordHasher
}

func NewUserService(repo UserRepository, companies CompanyLookup, sessions SessionRevoker, hasher PasswordHasher) *UserService {
	return &UserService{repo: repo, companies: companies, sessions: sessions, hasher: hasher}
}

// Create adds a user. Company users can only create users in their own
// company, with company roles.
func (s *UserService) Create(ctx context.Context, p auth.Principal, in CreateUserInput) (*UserDetails, error) {
	companyID := in.CompanyID
	if !p.IsPlatformUser() {
		// The tenant comes from the TOKEN, never from the request body.
		if companyID != nil && *companyID != *p.CompanyID {
			return nil, model.ErrNotFound // that company "does not exist" for you
		}
		companyID = p.CompanyID
	}

	// Normalize. The password is deliberately NOT trimmed: spaces can be part of it.
	email := strings.ToLower(strings.TrimSpace(in.Email))
	username := strings.ToLower(strings.TrimSpace(in.Username))
	firstName := strings.TrimSpace(in.FirstName)
	lastName := strings.TrimSpace(in.LastName)
	roles := uniqueStrings(in.Roles)

	// Role validation uses the TARGET user's company: a company user can only
	// get company roles, so platform_admin can never be granted from inside a
	// company (privilege escalation).
	if err := validateNewUser(companyID, email, username, in.Password, firstName, lastName, roles); err != nil {
		return nil, err
	}

	// A user must belong to a company that exists (and is not soft-deleted).
	if companyID != nil {
		if _, err := s.companies.GetByID(ctx, *companyID); errors.Is(err, model.ErrNotFound) {
			return nil, &validation.ValidationError{Fields: map[string]string{"company_id": "company does not exist"}}
		} else if err != nil {
			return nil, err
		}
	}

	// Hash only after validation: hashing is slow, no point doing it for bad input.
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	u, err := s.repo.Create(ctx, model.User{
		CompanyID:    companyID,
		Email:        nilIfEmpty(email),
		Username:     nilIfEmpty(username),
		PasswordHash: hash,
		FirstName:    firstName,
		LastName:     lastName,
	}, roles)
	if err != nil {
		return nil, err
	}
	// Return the roles that were actually saved (after de-duplication), so
	// callers never have to repeat that logic.
	return &UserDetails{User: u, Roles: roles}, nil
}

// Get returns a user with their roles, within the caller's tenant.
func (s *UserService) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*UserDetails, error) {
	u, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	roles, err := s.repo.GetRoles(ctx, []uuid.UUID{u.ID})
	if err != nil {
		return nil, err
	}
	return &UserDetails{User: u, Roles: roles[u.ID]}, nil
}

// List returns users: all (or one company's) for platform users, and only
// their own company's for company users.
func (s *UserService) List(ctx context.Context, p auth.Principal, params UserListParams) (*UserPage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})

	f := model.UserFilter{CompanyID: params.CompanyID}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID // forced: a company user cannot list other tenants
	}

	users, total, err := s.repo.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}

	// One query for all roles on this page (not one per user: N+1).
	ids := make([]uuid.UUID, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	roles, err := s.repo.GetRoles(ctx, ids)
	if err != nil {
		return nil, err
	}

	items := make([]UserDetails, len(users))
	for i := range users {
		items[i] = UserDetails{User: &users[i], Roles: roles[users[i].ID]}
	}
	return &UserPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

// Update changes names, active status and/or roles of a user in the caller's
// tenant. Nobody can change their own roles or active status.
func (s *UserService) Update(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateUserInput) (*UserDetails, error) {
	if in.FirstName == nil && in.LastName == nil && in.IsActive == nil && in.Roles == nil {
		return nil, &validation.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}
	// No self-escalation (granting yourself roles) and no self-lockout.
	if id == p.UserID && (in.Roles != nil || in.IsActive != nil) {
		return nil, model.ErrForbidden
	}

	u, err := s.getInScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	wasActive := u.IsActive

	var v validation.Validator
	if in.FirstName != nil {
		u.FirstName = strings.TrimSpace(*in.FirstName)
		v.Check(u.FirstName != "", "first_name", "is required")
		v.Check(utf8.RuneCountInString(u.FirstName) <= userNameMaxLen, "first_name", "must be at most 100 characters")
	}
	if in.LastName != nil {
		u.LastName = strings.TrimSpace(*in.LastName)
		v.Check(u.LastName != "", "last_name", "is required")
		v.Check(utf8.RuneCountInString(u.LastName) <= userNameMaxLen, "last_name", "must be at most 100 characters")
	}
	if in.IsActive != nil {
		u.IsActive = *in.IsActive
	}
	var roles []string // nil = unchanged
	if in.Roles != nil {
		roles = uniqueStrings(*in.Roles)
		checkRoles(&v, u.CompanyID, roles) // roles must fit the TARGET user's scope
	}
	if err := v.Err(); err != nil {
		return nil, err
	}

	updated, err := s.repo.Update(ctx, *u, roles)
	if err != nil {
		return nil, err
	}

	// Deactivation already removes all permissions immediately (see
	// GetPermissions); revoking sessions also kills their refresh tokens.
	if wasActive && !updated.IsActive {
		if err := s.sessions.RevokeAllForUser(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, p, id)
}

// Delete soft-deletes a user in the caller's tenant and ends their sessions.
func (s *UserService) Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	if id == p.UserID {
		return model.ErrForbidden // nobody deletes themselves
	}
	if _, err := s.getInScope(ctx, p, id); err != nil {
		return err
	}
	if err := s.repo.SoftDelete(ctx, id); err != nil {
		return err
	}
	return s.sessions.RevokeAllForUser(ctx, id)
}

// SetPassword sets another user's password: for users who cannot use
// "forgot password" (drivers with a username but no email) or are locked
// out. The user is logged out everywhere, so whoever knew the old password
// loses access. Tenant scope as for Update; nobody sets their own password
// here (that must require the current password).
func (s *UserService) SetPassword(ctx context.Context, p auth.Principal, id uuid.UUID, newPassword string) error {
	if id == p.UserID {
		return model.ErrForbidden
	}
	var v validation.Validator
	checkPassword(&v, "new_password", newPassword)
	if err := v.Err(); err != nil {
		return err
	}

	u, err := s.getInScope(ctx, p, id)
	if err != nil {
		return err
	}
	if !u.IsActive {
		return model.ErrUserInactive // activate the account first
	}

	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}
	if err := s.repo.UpdatePassword(ctx, id, hash); err != nil {
		return err
	}
	// After the change: a failure here returns an error and can simply be
	// retried (setting the same password again is harmless).
	return s.sessions.RevokeAllForUser(ctx, id)
}

// getInScope loads a user the caller is allowed to see. For company users,
// users of other companies and platform users "do not exist" (404).
func (s *UserService) getInScope(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.User, error) {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.IsPlatformUser() {
		return u, nil
	}
	if u.CompanyID == nil || *u.CompanyID != *p.CompanyID {
		return nil, model.ErrNotFound
	}
	return u, nil
}

// GetPermissions returns a user's effective permissions (empty for inactive
// or deleted users). Used by the permission middleware on every request.
func (s *UserService) GetPermissions(ctx context.Context, id uuid.UUID) ([]model.Permission, error) {
	return s.repo.GetPermissions(ctx, id)
}

// GetProfile returns the caller's own user, roles and permissions ("who am I?").
func (s *UserService) GetProfile(ctx context.Context, id uuid.UUID) (*UserProfile, error) {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// The access token may still be valid for a few minutes after
	// deactivation; the account itself is not usable any more.
	if !u.IsActive {
		return nil, model.ErrUserInactive
	}
	roles, err := s.repo.GetRoles(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	perms, err := s.repo.GetPermissions(ctx, id)
	if err != nil {
		return nil, err
	}
	return &UserProfile{User: u, Roles: roles[id], Permissions: perms}, nil
}

func validateNewUser(companyID *uuid.UUID, email, username, password, firstName, lastName string, roles []string) error {
	var v validation.Validator

	v.Check(email != "" || username != "", "login", "email or username is required")
	if email != "" {
		v.Check(validation.Email(email), "email", "must be a valid email address")
	}
	if username != "" {
		v.Check(usernameRe.MatchString(username), "username",
			"must be 3-50 characters: letters, digits, dot, underscore or hyphen")
	}

	checkPassword(&v, "password", password)

	v.Check(firstName != "", "first_name", "is required")
	v.Check(utf8.RuneCountInString(firstName) <= userNameMaxLen, "first_name", "must be at most 100 characters")
	v.Check(lastName != "", "last_name", "is required")
	v.Check(utf8.RuneCountInString(lastName) <= userNameMaxLen, "last_name", "must be at most 100 characters")

	checkRoles(&v, companyID, roles)

	return v.Err()
}

// checkPassword applies the password rules (user creation, password reset).
func checkPassword(v *validation.Validator, field, password string) {
	v.Check(utf8.RuneCountInString(password) >= passwordMinLen, field, "must be at least 8 characters")
	v.Check(len(password) <= auth.MaxPasswordBytes, field, "must be at most 72 bytes")
}

// checkRoles: at least one role, and every role must fit the user's scope
// (platform users: platform roles; company users: company roles).
func checkRoles(v *validation.Validator, companyID *uuid.UUID, roles []string) {
	v.Check(len(roles) > 0, "roles", "at least one role is required")
	allowed, kind := companyRoles, "company"
	if companyID == nil {
		allowed, kind = platformRoles, "platform"
	}
	for _, r := range roles {
		v.Check(allowed[r], "roles", fmt.Sprintf("role %q cannot be given to a %s user", r, kind))
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// uniqueStrings removes duplicates, keeping the first occurrence's order.
func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
