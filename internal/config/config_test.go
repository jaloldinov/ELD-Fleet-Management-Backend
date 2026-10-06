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
	t.Setenv("REDIS_URL", "")
	t.Setenv("SMTP_HOST", "")
	t.Setenv("SMTP_PORT", "")
	clearStripe(t)
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
	assert.Empty(t, cfg.RedisURL, "optional: rate limits count in memory without it")
	assert.Empty(t, cfg.SMTPHost, "optional: emails are only logged without it")
	assert.Equal(t, 587, cfg.SMTPPort)
	assert.False(t, cfg.BillingEnabled(), "optional: billing is off without Stripe keys")
	assert.False(t, cfg.IsProduction())
}

func TestLoad_FromEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("HTTP_PORT", "9000")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("ACCESS_TOKEN_TTL", "5m")
	t.Setenv("TRUSTED_PROXIES", " 10.0.0.0/8, 192.168.1.5,, ")
	t.Setenv("REDIS_URL", "redis://cache:6379/0")

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, "redis://cache:6379/0", cfg.RedisURL)
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

func TestLoad_SMTP(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_HOST", "smtp.gmail.com")
	t.Setenv("SMTP_PORT", "587")
	t.Setenv("SMTP_USERNAME", "me@gmail.com")
	t.Setenv("SMTP_PASSWORD", "abcd efgh ijkl mnop")
	t.Setenv("SMTP_FROM", "me@gmail.com")
	t.Setenv("SMTP_FROM_NAME", "ELD Fleet")

	cfg, err := Load()

	require.NoError(t, err)
	assert.Equal(t, "smtp.gmail.com", cfg.SMTPHost)
	assert.Equal(t, "me@gmail.com", cfg.SMTPFrom)
	assert.Equal(t, "ELD Fleet", cfg.SMTPFromName)
}

func TestLoad_SMTPInvalid(t *testing.T) {
	tests := map[string]map[string]string{
		"port not a number":     {"SMTP_PORT": "smtp"},
		"port out of range":     {"SMTP_HOST": "h", "SMTP_FROM": "a@b.co", "SMTP_PORT": "70000"},
		"missing from":          {"SMTP_HOST": "h", "SMTP_FROM": ""},
		"bad from":              {"SMTP_HOST": "h", "SMTP_FROM": "not an address"},
		"from with name":        {"SMTP_HOST": "h", "SMTP_FROM": "ELD <a@b.co>"},
		"user without password": {"SMTP_HOST": "h", "SMTP_FROM": "a@b.co", "SMTP_USERNAME": "u", "SMTP_PASSWORD": ""},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			for _, k := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM"} {
				t.Setenv(k, "")
			}
			for k, v := range env {
				t.Setenv(k, v)
			}

			_, err := Load()

			assert.Error(t, err)
		})
	}
}

func TestLoad_SMTPPasswordNeverInError(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_HOST", "h")
	t.Setenv("SMTP_FROM", "a@b.co")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "super-secret-app-password")

	_, err := Load()

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super-secret-app-password")
}

// Fake keys with the right shape. Never real ones in tests.
const (
	testStripeSecret  = "sk_test_51Fake0000000000000000000"
	testStripePublic  = "pk_test_51Fake0000000000000000000"
	testStripeWebhook = "whsec_fake000000000000000000000"
)

// clearStripe unsets the Stripe variables, which `make test` exports from
// .env (with the developer's real keys).
func clearStripe(t *testing.T) {
	t.Helper()
	for _, k := range []string{"STRIPE_SECRET_KEY", "STRIPE_PUBLISHABLE_KEY", "STRIPE_WEBHOOK_SECRET"} {
		t.Setenv(k, "")
	}
}

func TestLoad_Stripe(t *testing.T) {
	setRequired(t)
	clearStripe(t)
	t.Setenv("STRIPE_SECRET_KEY", testStripeSecret)
	t.Setenv("STRIPE_PUBLISHABLE_KEY", testStripePublic)
	t.Setenv("STRIPE_WEBHOOK_SECRET", testStripeWebhook)

	cfg, err := Load()

	require.NoError(t, err)
	assert.True(t, cfg.BillingEnabled())
	assert.True(t, cfg.StripeTestMode())
	assert.Equal(t, testStripeSecret, cfg.StripeSecretKey)
	assert.Equal(t, testStripePublic, cfg.StripePublishableKey)
	assert.Equal(t, testStripeWebhook, cfg.StripeWebhookSecret)
}

func TestLoad_StripeLiveKeys(t *testing.T) {
	live := map[string]string{
		"STRIPE_SECRET_KEY":      "sk_live_51Fake0000000000000000000",
		"STRIPE_PUBLISHABLE_KEY": "pk_live_51Fake0000000000000000000",
	}

	t.Run("refused in development", func(t *testing.T) {
		setRequired(t)
		clearStripe(t)
		t.Setenv("APP_ENV", "development")
		for k, v := range live {
			t.Setenv(k, v)
		}
		_, err := Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "live key")
	})

	t.Run("allowed in production", func(t *testing.T) {
		setRequired(t)
		clearStripe(t)
		t.Setenv("APP_ENV", "production")
		for k, v := range live {
			t.Setenv(k, v)
		}
		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.StripeTestMode())
	})
}

func TestLoad_StripeRestrictedKey(t *testing.T) {
	setRequired(t)
	clearStripe(t)
	t.Setenv("STRIPE_SECRET_KEY", "rk_test_51Fake0000000000000000000")
	t.Setenv("STRIPE_PUBLISHABLE_KEY", testStripePublic)

	cfg, err := Load()

	require.NoError(t, err)
	assert.True(t, cfg.StripeTestMode())
}

func TestLoad_StripeInvalid(t *testing.T) {
	tests := map[string]map[string]string{
		"publishable key as secret": {"STRIPE_SECRET_KEY": testStripePublic, "STRIPE_PUBLISHABLE_KEY": testStripePublic},
		"secret without mode":       {"STRIPE_SECRET_KEY": "sk_abc", "STRIPE_PUBLISHABLE_KEY": testStripePublic},
		"secret with nothing after": {"STRIPE_SECRET_KEY": "sk_test_", "STRIPE_PUBLISHABLE_KEY": testStripePublic},
		"missing publishable":       {"STRIPE_SECRET_KEY": testStripeSecret},
		"secret key as publishable": {"STRIPE_SECRET_KEY": testStripeSecret, "STRIPE_PUBLISHABLE_KEY": testStripeSecret},
		"test secret, live public": {
			"STRIPE_SECRET_KEY": testStripeSecret, "STRIPE_PUBLISHABLE_KEY": "pk_live_51Fake0000000000000000000",
		},
		"bad webhook secret": {
			"STRIPE_SECRET_KEY": testStripeSecret, "STRIPE_PUBLISHABLE_KEY": testStripePublic,
			"STRIPE_WEBHOOK_SECRET": "abc",
		},
		"publishable without secret": {"STRIPE_PUBLISHABLE_KEY": testStripePublic},
		"webhook without secret":     {"STRIPE_WEBHOOK_SECRET": testStripeWebhook},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			clearStripe(t)
			for k, v := range env {
				t.Setenv(k, v)
			}

			_, err := Load()

			assert.Error(t, err)
		})
	}
}

func TestLoad_StripeKeysNeverInError(t *testing.T) {
	setRequired(t)
	clearStripe(t)
	t.Setenv("STRIPE_SECRET_KEY", "sk_live_super-secret-value")
	t.Setenv("STRIPE_PUBLISHABLE_KEY", "pk_test_public-value")

	_, err := Load()

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super-secret-value")
}
