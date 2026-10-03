package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/google/uuid"
)

// ResetCodeDigits is the length of the emailed password-reset code.
const ResetCodeDigits = 6

// NewResetCode returns a random code of ResetCodeDigits digits, e.g. "048213".
// Every code is equally likely (crypto/rand, no modulo bias).
func NewResetCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("generate reset code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ResetCodeHasher hashes reset codes with a server-side key (HMAC-SHA256).
//
// Why a key: a 6-digit code has only 1,000,000 values. With a plain hash,
// anyone who reads the database computes all of them in a second and has
// every pending code. With HMAC they also need the key, which is not in the
// database.
type ResetCodeHasher struct {
	key []byte
}

// NewResetCodeHasher derives its key from secret (the JWT secret), so no
// new secret has to be configured. Deriving instead of reusing the secret
// as-is keeps the two uses apart: a code hash can never be mistaken for, or
// help forge, a token signature.
func NewResetCodeHasher(secret string) *ResetCodeHasher {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("eld/password-reset-code/v1"))
	return &ResetCodeHasher{key: mac.Sum(nil)}
}

// Hash binds the code to the user, so a code is only valid for its user.
func (h *ResetCodeHasher) Hash(userID uuid.UUID, code string) []byte {
	mac := hmac.New(sha256.New, h.key)
	mac.Write(userID[:])
	mac.Write([]byte(code))
	return mac.Sum(nil)
}

// Equal compares in constant time, so response timing reveals nothing
// about how much of a guess was right.
func (h *ResetCodeHasher) Equal(a, b []byte) bool {
	return hmac.Equal(a, b)
}

// NewResetToken returns the one-time token handed out after the code was
// verified. Same format as refresh tokens: 32 random bytes; only the SHA-256
// is stored (enough for random tokens, unlike for 6-digit codes).
func NewResetToken() (plain string, hash []byte, err error) {
	return NewRefreshToken()
}

// HashResetToken hashes a presented reset token for lookup.
func HashResetToken(plain string) []byte {
	return HashRefreshToken(plain)
}
