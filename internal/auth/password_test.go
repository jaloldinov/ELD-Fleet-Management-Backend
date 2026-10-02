package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// MinCost keeps these tests fast; the algorithm is the same.
var testHasher = NewBcryptHasher(bcrypt.MinCost)

func TestBcryptHasher_HashAndCompare(t *testing.T) {
	hash, err := testHasher.Hash("correct horse battery staple")

	require.NoError(t, err)
	assert.NotContains(t, hash, "correct horse", "hash must not contain the password")
	assert.True(t, testHasher.Compare(hash, "correct horse battery staple"))
	assert.False(t, testHasher.Compare(hash, "wrong password"))
	assert.False(t, testHasher.Compare("not-a-bcrypt-hash", "anything"))
}

func TestBcryptHasher_SaltMakesHashesDifferent(t *testing.T) {
	h1, err := testHasher.Hash("same password")
	require.NoError(t, err)
	h2, err := testHasher.Hash("same password")
	require.NoError(t, err)

	assert.NotEqual(t, h1, h2, "random salt: equal passwords must not give equal hashes")
}

func TestBcryptHasher_UsesConfiguredCost(t *testing.T) {
	hash, err := NewBcryptHasher(bcrypt.MinCost + 1).Hash("password")
	require.NoError(t, err)

	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, bcrypt.MinCost+1, cost)
}

func TestBcryptHasher_RejectsTooLongPassword(t *testing.T) {
	_, err := testHasher.Hash(strings.Repeat("a", MaxPasswordBytes+1))

	assert.Error(t, err, "bcrypt must not silently truncate")
}
