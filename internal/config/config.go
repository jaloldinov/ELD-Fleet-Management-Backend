// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"

	// JWTSecretMinBytes: HS256 keys shorter than the hash output (32 bytes)
	// weaken the signature and can be brute-forced offline from one token.
	JWTSecretMinBytes = 32
)

// Config holds every setting the application needs to start.
// It grows as we add new pieces (database, JWT, ...).
type Config struct {
	Env         string // EnvDevelopment or EnvProduction
	HTTPPort    string
	LogLevel    string // debug, info, warn, error
	DatabaseURL string // postgres://user:pass@host:port/db?sslmode=disable

	JWTSecret       string        // HMAC key for signing access tokens
	AccessTokenTTL  time.Duration // how long an access token is valid
	RefreshTokenTTL time.Duration // how long a refresh token (session) is valid
}

// Load reads the configuration from environment variables, applies defaults,
// and validates the result. It fails fast: a bad config should stop the app
// at startup, not cause strange behaviour later.
func Load() (*Config, error) {
	ttl, err := time.ParseDuration(getEnv("ACCESS_TOKEN_TTL", "15m"))
	if err != nil {
		return nil, fmt.Errorf("invalid config: ACCESS_TOKEN_TTL: %w", err)
	}
	refreshTTL, err := time.ParseDuration(getEnv("REFRESH_TOKEN_TTL", "720h")) // 30 days
	if err != nil {
		return nil, fmt.Errorf("invalid config: REFRESH_TOKEN_TTL: %w", err)
	}

	cfg := &Config{
		Env:      getEnv("APP_ENV", EnvDevelopment),
		HTTPPort: getEnv("HTTP_PORT", "8080"),
		LogLevel: getEnv("LOG_LEVEL", "info"),
		// No defaults for secrets/connections: a wrong database or a
		// well-known secret must never be used by accident.
		DatabaseURL:     getEnv("DATABASE_URL", ""),
		JWTSecret:       getEnv("JWT_SECRET", ""),
		AccessTokenTTL:  ttl,
		RefreshTokenTTL: refreshTTL,
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// IsProduction reports whether the app runs in production mode.
func (c *Config) IsProduction() bool {
	return c.Env == EnvProduction
}

func (c *Config) validate() error {
	switch c.Env {
	case EnvDevelopment, EnvProduction:
	default:
		return fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, c.Env)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", c.LogLevel)
	}

	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	// Never print the secret itself in an error message.
	if len(c.JWTSecret) < JWTSecretMinBytes {
		return fmt.Errorf("JWT_SECRET must be at least %d bytes (generate one with: openssl rand -base64 48)", JWTSecretMinBytes)
	}

	if c.AccessTokenTTL < time.Minute || c.AccessTokenTTL > time.Hour {
		return fmt.Errorf("ACCESS_TOKEN_TTL must be between 1m and 1h, got %s", c.AccessTokenTTL)
	}

	if c.RefreshTokenTTL < time.Hour || c.RefreshTokenTTL > 90*24*time.Hour {
		return fmt.Errorf("REFRESH_TOKEN_TTL must be between 1h and 2160h (90 days), got %s", c.RefreshTokenTTL)
	}

	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
