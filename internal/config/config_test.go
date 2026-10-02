package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDatabaseURL = "postgres://u:p@localhost:5432/db"
	testJWTSecret   = "0123456789abcdef0123456789abcdef" // exactly 32 bytes
)

// setRequired sets the variables that have no default.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", testDatabaseURL)
	t.Setenv("JWT_SECRET", testJWTSecret)
}

func TestLoad_Defaults(t *testing.T) {
	// t.Setenv restores the original value when the test ends.
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("ACCESS_TOKEN_TTL", "")
	t.Setenv("REFRESH_TOKEN_TTL", "")
	t.Setenv("TRUSTED_PROXIES", "")
	setRequired(t)

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, EnvDevelopment, cfg.Env)
	assert.Equal(t, "8080", cfg.HTTPPort)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	assert.Equal(t, testJWTSecret, cfg.JWTSecret)
	assert.Equal(t, 15*time.Minute, cfg.AccessTokenTTL)
	assert.Equal(t, 30*24*time.Hour, cfg.RefreshTokenTTL)
	assert.Empty(t, cfg.TrustedProxies, "trust no proxy unless told to")
	assert.False(t, cfg.IsProduction())
}

func TestLoad_FromEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("HTTP_PORT", "9000")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("ACCESS_TOKEN_TTL", "5m")
	t.Setenv("TRUSTED_PROXIES", " 10.0.0.0/8, 192.168.1.5,, ")

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, EnvProduction, cfg.Env)
	assert.Equal(t, "9000", cfg.HTTPPort)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, 5*time.Minute, cfg.AccessTokenTTL)
	assert.Equal(t, []string{"10.0.0.0/8", "192.168.1.5"}, cfg.TrustedProxies, "trimmed, empty items dropped")
	assert.True(t, cfg.IsProduction())
}

func TestLoad_Invalid(t *testing.T) {
	// Table-driven test: one test function, many cases.
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "unknown env", key: "APP_ENV", value: "staging"},
		{name: "unknown log level", key: "LOG_LEVEL", value: "verbose"},
		{name: "missing database url", key: "DATABASE_URL", value: ""},
		{name: "missing jwt secret", key: "JWT_SECRET", value: ""},
		{name: "short jwt secret", key: "JWT_SECRET", value: strings.Repeat("x", 31)},
		{name: "unparsable ttl", key: "ACCESS_TOKEN_TTL", value: "15 minutes"},
		{name: "ttl too short", key: "ACCESS_TOKEN_TTL", value: "10s"},
		{name: "ttl too long", key: "ACCESS_TOKEN_TTL", value: "24h"},
		{name: "refresh ttl too short", key: "REFRESH_TOKEN_TTL", value: "30m"},
		{name: "refresh ttl too long", key: "REFRESH_TOKEN_TTL", value: "2400h"},
		{name: "trusted proxy not an ip", key: "TRUSTED_PROXIES", value: "10.0.0.1, lb.internal"},
		{name: "trusted proxy bad cidr", key: "TRUSTED_PROXIES", value: "10.0.0.0/33"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)             // valid baseline...
			t.Setenv(tt.key, tt.value) // ...then break one value

			cfg, err := Load()

			assert.Error(t, err)
			assert.Nil(t, cfg)
		})
	}
}

func TestLoad_ErrorNeverContainsSecret(t *testing.T) {
	setRequired(t)
	t.Setenv("JWT_SECRET", "my-short-secret")

	_, err := Load()

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "my-short-secret")
}
