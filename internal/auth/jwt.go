package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const tokenIssuer = "eld-api"

// ErrInvalidToken is returned for every kind of bad token (malformed, wrong
// signature, expired, wrong algorithm...). Callers only need to know "no".
var ErrInvalidToken = errors.New("invalid token")

// accessClaims is the JWT payload. It is only base64-encoded, NOT encrypted:
// anyone holding the token can read it, so it contains IDs only, no secrets.
type accessClaims struct {
	CompanyID            string `json:"cid,omitempty"` // empty for platform users
	jwt.RegisteredClaims        // sub, iss, exp, iat, jti
}

// TokenClaims is what a verified access token tells us.
type TokenClaims struct {
	UserID    uuid.UUID
	CompanyID *uuid.UUID // nil = platform user
	ExpiresAt time.Time
}

// TokenManager issues and verifies HS256-signed access tokens.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time // replaceable in tests to simulate time passing
}

func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), ttl: ttl, now: time.Now}
}

// Issue creates a signed access token for a user.
func (m *TokenManager) Issue(userID uuid.UUID, companyID *uuid.UUID) (token string, expiresAt time.Time, err error) {
	now := m.now()
	expiresAt = now.Add(m.ttl)

	claims := accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			Issuer:    tokenIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(), // jti: unique id, useful for audit/revocation later
		},
	}
	if companyID != nil {
		claims.CompanyID = companyID.String()
	}

	token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return token, expiresAt, nil
}

// Parse verifies a token's signature and claims and returns what it says.
func (m *TokenManager) Parse(token string) (*TokenClaims, error) {
	var c accessClaims
	_, err := jwt.ParseWithClaims(token, &c,
		func(*jwt.Token) (any, error) { return m.secret, nil },
		// Accept ONLY HS256. Without this, a token whose header says
		// "alg":"none" (unsigned) or another algorithm could be accepted.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(m.now),
	)
	if err != nil {
		// Keep the library's reason for logs, but callers match ErrInvalidToken.
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	userID, err := uuid.Parse(c.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: bad subject", ErrInvalidToken)
	}

	out := &TokenClaims{UserID: userID, ExpiresAt: c.ExpiresAt.Time}
	if c.CompanyID != "" {
		companyID, err := uuid.Parse(c.CompanyID)
		if err != nil {
			return nil, fmt.Errorf("%w: bad company id", ErrInvalidToken)
		}
		out.CompanyID = &companyID
	}
	return out, nil
}
