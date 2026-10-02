package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// AuthUserRepository is what AuthService needs from the user data layer.
type AuthUserRepository interface {
	GetByLogin(ctx context.Context, login string) (*model.User, error)
	UpdateLastLogin(ctx context.Context, id uuid.UUID) error
}

// PasswordChecker hashes and compares passwords (auth.BcryptHasher).
type PasswordChecker interface {
	Hash(password string) (string, error)
	Compare(hash, password string) bool
}

// TokenIssuer creates access tokens (auth.TokenManager).
type TokenIssuer interface {
	Issue(userID uuid.UUID, companyID *uuid.UUID) (token string, expiresAt time.Time, err error)
}

// LoginResult is what a successful login returns.
type LoginResult struct {
	AccessToken string
	ExpiresAt   time.Time
}

type AuthService struct {
	users  AuthUserRepository
	hasher PasswordChecker
	tokens TokenIssuer

	// dummyHash is compared against when the user does not exist, so that
	// "unknown user" takes as long as "wrong password" (see Login).
	dummyHash string
}

func NewAuthService(users AuthUserRepository, hasher PasswordChecker, tokens TokenIssuer) (*AuthService, error) {
	// Hashed with the real hasher, so it costs exactly as much as a real check.
	dummy, err := hasher.Hash("dummy password used only to equalize timing")
	if err != nil {
		return nil, fmt.Errorf("create dummy hash: %w", err)
	}
	return &AuthService{users: users, hasher: hasher, tokens: tokens, dummyHash: dummy}, nil
}

// Login checks credentials and returns an access token.
//
// Security properties:
//   - "unknown user" and "wrong password" return the SAME error...
//   - ...and take the SAME time (bcrypt runs in both cases), so neither the
//     message nor the response time reveals whether an account exists.
//   - "account disabled" is only revealed AFTER the correct password.
func (s *AuthService) Login(ctx context.Context, login, password string) (*LoginResult, error) {
	u, err := s.users.GetByLogin(ctx, login)
	if errors.Is(err, model.ErrNotFound) {
		s.hasher.Compare(s.dummyHash, password) // burn the same CPU time; result ignored
		return nil, model.ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	if !s.hasher.Compare(u.PasswordHash, password) {
		return nil, model.ErrInvalidCredentials
	}

	if !u.IsActive {
		return nil, model.ErrUserInactive
	}

	token, expiresAt, err := s.tokens.Issue(u.ID, u.CompanyID)
	if err != nil {
		return nil, fmt.Errorf("issue token: %w", err)
	}

	// Bookkeeping only: a failure here must not block a valid login.
	// (5c-2 will record it together with the refresh token.)
	_ = s.users.UpdateLastLogin(ctx, u.ID)

	return &LoginResult{AccessToken: token, ExpiresAt: expiresAt}, nil
}
