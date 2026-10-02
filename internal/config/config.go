// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"os"
)

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config holds every setting the application needs to start.
// It grows as we add new pieces (database, JWT, ...).
type Config struct {
	Env      string // EnvDevelopment or EnvProduction
	HTTPPort string
	LogLevel string // debug, info, warn, error
}

// Load reads the configuration from environment variables, applies defaults,
// and validates the result. It fails fast: a bad config should stop the app
// at startup, not cause strange behaviour later.
func Load() (*Config, error) {
	cfg := &Config{
		Env:      getEnv("APP_ENV", EnvDevelopment),
		HTTPPort: getEnv("HTTP_PORT", "8080"),
		LogLevel: getEnv("LOG_LEVEL", "info"),
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

	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
