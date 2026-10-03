// Package config loads application settings from environment variables.
package config

import (
	"fmt"
	"net"
	"net/mail"
	"os"
	"strconv"
	"strings"
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

	// RedisURL (redis://host:port/db) makes the rate limiters count in Redis,
	// shared by every API instance. Optional: empty = count in memory, which
	// is only correct with a single instance. Set it in production.
	RedisURL string

	// SMTP sends emails (password reset codes). Optional: without SMTPHost,
	// emails are not sent (only logged), so "forgot password" cannot work.
	// Gmail: smtp.gmail.com, port 587, the Gmail address as user and From,
	// and a 16-character App Password (not the account password).
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string // empty = no authentication (Mailpit in development)
	SMTPPassword string
	SMTPFrom     string // plain address: you@gmail.com
	SMTPFromName string // shown as the sender's name; default "ELD"

	JWTSecret       string        // HMAC key for signing access tokens
	AccessTokenTTL  time.Duration // how long an access token is valid
	RefreshTokenTTL time.Duration // how long a refresh token (session) is valid

	// TrustedProxies are the load balancers / reverse proxies (IPs or CIDRs)
	// whose X-Forwarded-For header we believe. Empty = trust no one and use
	// the TCP peer address as the client IP.
	TrustedProxies []string
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
	smtpPort, err := strconv.Atoi(getEnv("SMTP_PORT", "587"))
	if err != nil {
		return nil, fmt.Errorf("invalid config: SMTP_PORT must be a number")
	}

	cfg := &Config{
		Env:      getEnv("APP_ENV", EnvDevelopment),
		HTTPPort: getEnv("HTTP_PORT", "8080"),
		LogLevel: getEnv("LOG_LEVEL", "info"),
		// No defaults for secrets/connections: a wrong database or a
		// well-known secret must never be used by accident.
		DatabaseURL:     getEnv("DATABASE_URL", ""),
		RedisURL:        getEnv("REDIS_URL", ""),
		SMTPHost:        getEnv("SMTP_HOST", ""),
		SMTPPort:        smtpPort,
		SMTPUsername:    getEnv("SMTP_USERNAME", ""),
		SMTPPassword:    getEnv("SMTP_PASSWORD", ""),
		SMTPFrom:        getEnv("SMTP_FROM", ""),
		SMTPFromName:    getEnv("SMTP_FROM_NAME", "ELD"),
		JWTSecret:       getEnv("JWT_SECRET", ""),
		AccessTokenTTL:  ttl,
		RefreshTokenTTL: refreshTTL,
		TrustedProxies:  splitList(getEnv("TRUSTED_PROXIES", "")),
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

	if c.SMTPHost != "" {
		if c.SMTPPort < 1 || c.SMTPPort > 65535 {
			return fmt.Errorf("SMTP_PORT must be between 1 and 65535, got %d", c.SMTPPort)
		}
		// A plain address: "Name <addr>" would break shells that source
		// .env (the < is a redirection); the name is SMTP_FROM_NAME.
		if a, err := mail.ParseAddress(c.SMTPFrom); err != nil || a.Address != c.SMTPFrom {
			return fmt.Errorf("SMTP_FROM must be a plain email address like you@gmail.com (the name goes in SMTP_FROM_NAME), got %q", c.SMTPFrom)
		}
		// Never print the password itself.
		if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
			return fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must be set together")
		}
	}

	for _, p := range c.TrustedProxies {
		if net.ParseIP(p) == nil {
			if _, _, err := net.ParseCIDR(p); err != nil {
				return fmt.Errorf("TRUSTED_PROXIES: %q is not an IP address or CIDR", p)
			}
		}
	}

	return nil
}

// splitList splits a comma-separated value, trimming spaces and dropping
// empty items: "a, b,," -> [a b].
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
