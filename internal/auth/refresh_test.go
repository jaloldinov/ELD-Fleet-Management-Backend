package auth

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRefreshToken(t *testing.T) {
	plain, hash, err := NewRefreshToken()
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(plain)
	require.NoError(t, err, "token must be URL-safe base64")
	assert.Len(t, raw, 32, "256 bits of randomness")
	assert.Len(t, hash, 32, "SHA-256 output")
	assert.Equal(t, HashRefreshToken(plain), hash, "returned hash matches the token")
}

func TestNewRefreshToken_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		plain, _, err := NewRefreshToken()
		require.NoError(t, err)
		require.False(t, seen[plain], "duplicate token generated")
		seen[plain] = true
	}
}

func TestHashRefreshToken_Deterministic(t *testing.T) {
	assert.Equal(t, HashRefreshToken("abc"), HashRefreshToken("abc"), "same input, same hash: needed for lookup")
	assert.NotEqual(t, HashRefreshToken("abc"), HashRefreshToken("abd"))
}
