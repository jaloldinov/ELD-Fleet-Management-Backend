package auth

import (
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewResetCode_SixDigits(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := NewResetCode()
		require.NoError(t, err)
		assert.Regexp(t, regexp.MustCompile(`^[0-9]{6}$`), code, "leading zeros kept")
		seen[code] = true
	}
	assert.Greater(t, len(seen), 190, "random, not repeating")
}

func TestResetCodeHasher(t *testing.T) {
	h := NewResetCodeHasher("server-secret-server-secret-1234")
	user := uuid.New()

	a := h.Hash(user, "123456")

	assert.True(t, h.Equal(a, h.Hash(user, "123456")), "deterministic")
	assert.False(t, h.Equal(a, h.Hash(user, "123457")), "other code")
	assert.False(t, h.Equal(a, h.Hash(uuid.New(), "123456")), "bound to the user")
	assert.False(t, h.Equal(a, NewResetCodeHasher("another-secret-another-secret-12").Hash(user, "123456")),
		"useless without the server key")
	assert.Len(t, a, 32)
}

func TestResetToken(t *testing.T) {
	plain, hash, err := NewResetToken()
	require.NoError(t, err)

	assert.Equal(t, hash, HashResetToken(plain))
	assert.NotEqual(t, hash, HashResetToken(plain+"x"))
}
