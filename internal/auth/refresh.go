package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// refreshTokenBytes: 32 random bytes = 256 bits. Guessing one is impossible.
const refreshTokenBytes = 32

// NewRefreshToken returns a new random token for the client and its SHA-256
// hash for the database. The plain token is never stored.
func NewRefreshToken() (plain string, hash []byte, err error) {
	b := make([]byte, refreshTokenBytes)
	// crypto/rand (NOT math/rand): unpredictable, suitable for secrets.
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	// URL-safe base64 without padding: safe in JSON, headers and URLs.
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashRefreshToken(plain), nil
}

// HashRefreshToken returns the value stored in and looked up from the database.
//
// SHA-256 (fast) is correct here, unlike for passwords: the token has 256 bits
// of randomness, so brute-forcing a leaked hash is impossible, and the hash
// must be deterministic (no salt) so we can find the row by it.
func HashRefreshToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
