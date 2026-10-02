package service

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

const (
	passwordMinLen  = 8 // characters (NIST SP 800-63B minimum)
	userNameMaxLen  = 100
	userEmailMaxLen = 254 // practical maximum length of an email address
)

// Same rule as the users_username_format CHECK constraint (after lowercasing).
var usernameRe = regexp.MustCompile(`^[a-z0-9._-]{3,50}$`)

// Which system roles each kind of user may have. The database cannot enforce
// this (it spans users.company_id and roles.scope), so the service does.
var (
	platformRoles = map[string]bool{model.RolePlatformAdmin: true}
	companyRoles  = map[string]bool{
		model.RoleAdmin: true, model.RoleManager: true, model.RoleDispatcher: true, model.RoleDriver: true,
	}
)

// UserRepository is what UserService needs from the data layer.
type UserRepository interface {
	Create(ctx context.Context, u model.User, roleNames []string) (*model.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	GetPermissions(ctx context.Context, userID uuid.UUID) ([]model.Permission, error)
}

// UserProfile is a user together with their effective permissions.
type UserProfile struct {
	User        *model.User
	Permissions []model.Permission
}

// PasswordHasher hashes passwords. auth.BcryptHasher in production; tests use
// a fast fake, because real bcrypt is slow on purpose.
type PasswordHasher interface {
	Hash(password string) (string, error)
}

type CreateUserInput struct {
	CompanyID *uuid.UUID // nil = platform user
	Email     string     // email and/or username; at least one
	Username  string
	Password  string
	FirstName string
	LastName  string
	Roles     []string // system role names, e.g. model.RoleDispatcher
}

type UserService struct {
	repo   UserRepository
	hasher PasswordHasher
}

func NewUserService(repo UserRepository, hasher PasswordHasher) *UserService {
	return &UserService{repo: repo, hasher: hasher}
}

func (s *UserService) Create(ctx context.Context, in CreateUserInput) (*model.User, error) {
	// Normalize. The password is deliberately NOT trimmed: spaces can be part of it.
	email := strings.ToLower(strings.TrimSpace(in.Email))
	username := strings.ToLower(strings.TrimSpace(in.Username))
	firstName := strings.TrimSpace(in.FirstName)
	lastName := strings.TrimSpace(in.LastName)
	roles := uniqueStrings(in.Roles)

	if err := validateNewUser(in.CompanyID, email, username, in.Password, firstName, lastName, roles); err != nil {
		return nil, err
	}

	// Hash only after validation: hashing is slow, no point doing it for bad input.
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	return s.repo.Create(ctx, model.User{
		CompanyID:    in.CompanyID,
		Email:        nilIfEmpty(email),
		Username:     nilIfEmpty(username),
		PasswordHash: hash,
		FirstName:    firstName,
		LastName:     lastName,
	}, roles)
}

func (s *UserService) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	return s.repo.GetByID(ctx, id)
}

// GetProfile returns a user and their permissions (for "who am I?").
func (s *UserService) GetProfile(ctx context.Context, id uuid.UUID) (*UserProfile, error) {
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	perms, err := s.repo.GetPermissions(ctx, id)
	if err != nil {
		return nil, err
	}
	return &UserProfile{User: u, Permissions: perms}, nil
}

func validateNewUser(companyID *uuid.UUID, email, username, password, firstName, lastName string, roles []string) error {
	var v model.Validator

	v.Check(email != "" || username != "", "login", "email or username is required")
	if email != "" {
		v.Check(isValidEmail(email), "email", "must be a valid email address")
	}
	if username != "" {
		v.Check(usernameRe.MatchString(username), "username",
			"must be 3-50 characters: letters, digits, dot, underscore or hyphen")
	}

	v.Check(utf8.RuneCountInString(password) >= passwordMinLen, "password", "must be at least 8 characters")
	v.Check(len(password) <= auth.MaxPasswordBytes, "password", "must be at most 72 bytes")

	v.Check(firstName != "", "first_name", "is required")
	v.Check(utf8.RuneCountInString(firstName) <= userNameMaxLen, "first_name", "must be at most 100 characters")
	v.Check(lastName != "", "last_name", "is required")
	v.Check(utf8.RuneCountInString(lastName) <= userNameMaxLen, "last_name", "must be at most 100 characters")

	v.Check(len(roles) > 0, "roles", "at least one role is required")
	allowed, kind := companyRoles, "company"
	if companyID == nil {
		allowed, kind = platformRoles, "platform"
	}
	for _, r := range roles {
		v.Check(allowed[r], "roles", fmt.Sprintf("role %q cannot be given to a %s user", r, kind))
	}

	return v.Err()
}

// isValidEmail accepts a plain address like "jane@acme.com". mail.ParseAddress
// also accepts forms like "Jane <jane@acme.com>", so we require the parsed
// address to equal the input. The domain must contain a dot (same as the DB
// CHECK constraint).
func isValidEmail(email string) bool {
	if len(email) > userEmailMaxLen {
		return false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return false
	}
	at := strings.LastIndex(email, "@")
	return strings.Contains(email[at+1:], ".")
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
