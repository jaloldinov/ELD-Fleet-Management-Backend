package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// DeviceKeyPrefix starts every device key. Like GitHub's "ghp_": a key
// pasted into a log, a chat or a repository is recognisable at a glance,
// and secret scanners can find it automatically.
const DeviceKeyPrefix = "eldk_"

// deviceKeyBytes: 256 random bits, like refresh tokens.
const deviceKeyBytes = 32

// NewDeviceKey returns a new key for the device and its SHA-256 hash for
// the database. The plain key is shown once and never stored.
func NewDeviceKey() (plain string, hash []byte, err error) {
	b := make([]byte, deviceKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate device key: %w", err)
	}
	plain = DeviceKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	return plain, HashDeviceKey(plain), nil
}

// HashDeviceKey returns the value stored in and looked up from the
// database. A fast, unsalted SHA-256 is right here (not bcrypt): the key is
// 256 random bits, so a leaked hash cannot be brute-forced, and the hash
// must be deterministic to find the device by it.
//
// It also makes a constant-time comparison unnecessary: we never compare
// the secret itself, the database looks up its hash. An attacker who times
// the lookup learns about hashes of keys they chose, which says nothing
// about any real key.
func HashDeviceKey(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// LooksLikeDeviceKey is a cheap format check before any database work.
func LooksLikeDeviceKey(s string) bool {
	return strings.HasPrefix(s, DeviceKeyPrefix) &&
		len(s) == len(DeviceKeyPrefix)+base64.RawURLEncoding.EncodedLen(deviceKeyBytes)
}

// Device is the ELD device calling the API: the device's badge, as
// Principal is a user's. It comes from a verified device key, never from
// the request body. A device is no user: it has no roles and no
// permissions, only the device endpoints accept it.
type Device struct {
	DeviceID  uuid.UUID
	CompanyID uuid.UUID
}

type deviceKey struct{}

// WithDevice returns a copy of ctx carrying d.
func WithDevice(ctx context.Context, d Device) context.Context {
	return context.WithValue(ctx, deviceKey{}, d)
}

// DeviceFromContext returns the calling device, if ctx carries one.
func DeviceFromContext(ctx context.Context) (Device, bool) {
	d, ok := ctx.Value(deviceKey{}).(Device)
	return d, ok
}
