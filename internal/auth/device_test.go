package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDeviceKey(t *testing.T) {
	plain, hash, err := NewDeviceKey()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(plain, "eldk_"))
	assert.True(t, LooksLikeDeviceKey(plain))
	assert.Len(t, hash, 32)
	assert.Equal(t, hash, HashDeviceKey(plain), "deterministic: the device is found by it")

	other, _, err := NewDeviceKey()
	require.NoError(t, err)
	assert.NotEqual(t, plain, other)
}

func TestLooksLikeDeviceKey(t *testing.T) {
	plain, _, err := NewDeviceKey()
	require.NoError(t, err)

	assert.False(t, LooksLikeDeviceKey(""))
	assert.False(t, LooksLikeDeviceKey(strings.TrimPrefix(plain, "eldk_")), "no prefix")
	assert.False(t, LooksLikeDeviceKey(plain[:len(plain)-1]), "too short")
	assert.False(t, LooksLikeDeviceKey(plain+"x"), "too long")
}

func TestDeviceContext(t *testing.T) {
	_, ok := DeviceFromContext(context.Background())
	assert.False(t, ok)

	d := Device{DeviceID: uuid.New(), CompanyID: uuid.New()}
	got, ok := DeviceFromContext(WithDevice(context.Background(), d))
	require.True(t, ok)
	assert.Equal(t, d, got)

	_, isUser := PrincipalFromContext(WithDevice(context.Background(), d))
	assert.False(t, isUser, "a device is never a user")
}
