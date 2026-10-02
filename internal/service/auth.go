package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// AuthUserRepository is what AuthService needs from the user data layer.
type AuthUserRepository interface {
	GetByLogin(ctx context.Context, login string) (*model.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
}

// SessionRepository stores refresh tokens (repository.SessionRepository).
type SessionRepository interface {
	CreateForLogin(ctx context.Context, t model.RefreshToken) error
	Rotate(ctx context.Context, presentedHash []byte, next model.RefreshToken) (uuid.UUID, error)
	Revoke(ctx context.Context, tokenHash []byte) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
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

// TokenPair is returned by login and refresh.
type TokenPair struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string // plain value: given to the client once, never stored
	RefreshExpiresAt time.Time
}

type AuthService struct {
	users      AuthUserRepository
	sessions   SessionRepository
	hasher     PasswordChecker
	tokens     TokenIssuer
	refreshTTL time.Duration

	// dummyHash is compared against when the user does not exist, so that
	// "unknown user" takes as long as "wrong password" (see Login).
	dummyHash string
}

func NewAuthService(
	users AuthUserRepository,
	sessions SessionRepository,
	hasher PasswordChecker,
	tokens TokenIssuer,
	refreshTTL time.Duration,
) (*AuthService, error) {
	// Hashed with the real hasher, so it costs exactly as much as a real check.
	dummy, err := hasher.Hash("dummy password used only to equalize timing")
	if err != nil {
		return nil, fmt.Errorf("create dummy hash: %w", err)
	}
	return &AuthService{
		users: users, sessions: sessions, hasher: hasher, tokens: tokens,
		refreshTTL: refreshTTL, dummyHash: dummy,
	}, nil
}

// Login checks credentials and starts a new session.
//
// Security properties:
//   - "unknown user" and "wrong password" return the SAME error...
//   - ...and take the SAME time (bcrypt runs in both cases), so neither the
//     message nor the response time reveals whether an account exists.
//   - "account disabled" is only revealed AFTER the correct password.
func (s *AuthService) Login(ctx context.Context, login, password string, client model.ClientInfo) (*TokenPair, error) {
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

	refreshPlain, refresh, err := s.newRefreshToken(client)
	if err != nil {
		return nil, err
	}
	refresh.UserID = u.ID
	// Stores the refresh token AND records last_login_at in one transaction.
	if err := s.sessions.CreateForLogin(ctx, refresh); err != nil {
		return nil, err
	}

	return s.tokenPair(u, refreshPlain, refresh.ExpiresAt)
}

// Refresh exchanges a refresh token for a new token pair (rotation).
// The presented token stops working; reusing it later revokes every session.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string, client model.ClientInfo) (*TokenPair, error) {
	nextPlain, next, err := s.newRefreshToken(client)
	if err != nil {
		return nil, err
	}

	userID, err := s.sessions.Rotate(ctx, auth.HashRefreshToken(refreshToken), next)
	if err != nil {
		return nil, err // ErrRefreshTokenInvalid / ErrRefreshTokenReused / db error
	}

	// The token was valid, but the user may have been deactivated or deleted
	// since login. Then end all their sessions instead of extending them.
	u, err := s.users.GetByID(ctx, userID)
	if errors.Is(err, model.ErrNotFound) {
		_ = s.sessions.RevokeAllForUser(ctx, userID)
		return nil, model.ErrRefreshTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if !u.IsActive {
		if err := s.sessions.RevokeAllForUser(ctx, userID); err != nil {
			return nil, err
		}
		return nil, model.ErrUserInactive
	}

	return s.tokenPair(u, nextPlain, next.ExpiresAt)
}

// Logout ends the session of the given refresh token. Unknown tokens are not
// an error: logout is idempotent and must not reveal whether a token existed.
// The access token stays valid until it expires (at most ACCESS_TOKEN_TTL).
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	return s.sessions.Revoke(ctx, auth.HashRefreshToken(refreshToken))
}

func (s *AuthService) newRefreshToken(client model.ClientInfo) (string, model.RefreshToken, error) {
	plain, hash, err := auth.NewRefreshToken()
	if err != nil {
		return "", model.RefreshToken{}, err
	}
	return plain, model.RefreshToken{
		TokenHash: hash,
		ExpiresAt: time.Now().Add(s.refreshTTL),
		UserAgent: client.UserAgent,
		IP:        client.IP,
	}, nil
}

func (s *AuthService) tokenPair(u *model.User, refreshPlain string, refreshExpiresAt time.Time) (*TokenPair, error) {
	access, accessExp, err := s.tokens.Issue(u.ID, u.CompanyID)
	if err != nil {
		return nil, fmt.Errorf("issue token: %w", err)
	}
	return &TokenPair{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refreshPlain, RefreshExpiresAt: refreshExpiresAt,
	}, nil
}
