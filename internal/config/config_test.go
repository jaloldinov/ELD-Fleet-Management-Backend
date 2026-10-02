package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDatabaseURL = "postgres://u:p@localhost:5432/db"

func TestLoad_Defaults(t *testing.T) {
	// t.Setenv restores the original value when the test ends.
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("DATABASE_URL", testDatabaseURL)

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, EnvDevelopment, cfg.Env)
	assert.Equal(t, "8080", cfg.HTTPPort)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, testDatabaseURL, cfg.DatabaseURL)
	assert.False(t, cfg.IsProduction())
}

func TestLoad_FromEnv(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("HTTP_PORT", "9000")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("DATABASE_URL", testDatabaseURL)

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, EnvProduction, cfg.Env)
	assert.Equal(t, "9000", cfg.HTTPPort)
	assert.Equal(t, "debug", cfg.LogLevel)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", testDatabaseURL) // valid baseline...
			t.Setenv(tt.key, tt.value)                // ...then break one value

			cfg, err := Load()

			assert.Error(t, err)
			assert.Nil(t, cfg)
		})
	}
}
