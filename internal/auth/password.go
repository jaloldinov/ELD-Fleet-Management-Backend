// Package auth contains authentication building blocks: password hashing and
// (later) token handling.
package auth

import "golang.org/x/crypto/bcrypt"

const (
	// DefaultBcryptCost: each +1 doubles the work. 12 takes ~250ms on a modern
	// CPU: unnoticeable at login, very expensive for an attacker who stole the
	// hashes and tries billions of guesses.
	DefaultBcryptCost = 12

	// MaxPasswordBytes: bcrypt only uses the first 72 bytes of the input.
	// Longer passwords are rejected (by validation) instead of silently cut.
	MaxPasswordBytes = 72
)

// BcryptHasher hashes and checks passwords with bcrypt.
// bcrypt generates a random salt per hash and stores it inside the hash
// string, so equal passwords produce different hashes.
type BcryptHasher struct {
	cost int
}

func NewBcryptHasher(cost int) *BcryptHasher {
	return &BcryptHasher{cost: cost}
}

func (h *BcryptHasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Compare reports whether password matches hash. The comparison runs in
// constant time, so response timing does not reveal how much matched.
func (h *BcryptHasher) Compare(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
