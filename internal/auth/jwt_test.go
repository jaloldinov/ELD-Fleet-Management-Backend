package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret-test-secret-test-secret!!"

// newTestManager returns a manager whose clock we control.
func newTestManager(now *time.Time) *TokenManager {
	m := NewTokenManager(testSecret, 15*time.Minute)
	m.now = func() time.Time { return *now }
	return m
}

func TestTokenManager_RoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newTestManager(&now)
	userID, companyID := uuid.New(), uuid.New()

	t.Run("company user", func(t *testing.T) {
		token, exp, err := m.Issue(userID, &companyID)
		require.NoError(t, err)
		assert.Equal(t, now.Add(15*time.Minute), exp)

		claims, err := m.Parse(token)

		require.NoError(t, err)
		assert.Equal(t, userID, claims.UserID)
		require.NotNil(t, claims.CompanyID)
		assert.Equal(t, companyID, *claims.CompanyID)
	})

	t.Run("platform user has no company", func(t *testing.T) {
		token, _, err := m.Issue(userID, nil)
		require.NoError(t, err)

		claims, err := m.Parse(token)

		require.NoError(t, err)
		assert.Nil(t, claims.CompanyID)
	})
}

func TestTokenManager_Expiry(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := newTestManager(&now)
	token, _, err := m.Issue(uuid.New(), nil)
	require.NoError(t, err)

	now = now.Add(14 * time.Minute) // still valid
	_, err = m.Parse(token)
	assert.NoError(t, err)

	now = now.Add(2 * time.Minute) // 16 minutes after issue: expired
	_, err = m.Parse(token)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestTokenManager_RejectsBadTokens(t *testing.T) {
	now := time.Now()
	m := newTestManager(&now)
	valid, _, err := m.Issue(uuid.New(), nil)
	require.NoError(t, err)

	claims := jwt.MapClaims{
		"sub": uuid.NewString(), "iss": tokenIssuer,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}

	// alg "none": an unsigned token. Classic attack against careless parsers.
	noneToken, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	// Correct secret, but a different algorithm than the one we allow.
	hs512Token, err := jwt.NewWithClaims(jwt.SigningMethodHS512, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)

	// Valid structure, signed with someone else's secret.
	foreignToken, _, err := NewTokenManager("another-secret-another-secret-12345", time.Hour).Issue(uuid.New(), nil)
	require.NoError(t, err)

	// Wrong issuer.
	otherIssuer := jwt.MapClaims{"sub": uuid.NewString(), "iss": "evil", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	otherIssuerToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, otherIssuer).SignedString([]byte(testSecret))
	require.NoError(t, err)

	// Payload changed after signing (e.g. attacker swaps in another user id).
	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + parts[1] + "x" + "." + parts[2]

	tests := map[string]string{
		"garbage":        "not.a.jwt",
		"empty":          "",
		"alg none":       noneToken,
		"alg HS512":      hs512Token,
		"foreign secret": foreignToken,
		"wrong issuer":   otherIssuerToken,
		"tampered":       tampered,
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := m.Parse(token)
			assert.ErrorIs(t, err, ErrInvalidToken)
		})
	}
}
