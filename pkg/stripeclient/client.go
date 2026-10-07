// Package stripeclient builds the Stripe API client with our settings and
// checks at startup that Stripe accepts the key.
//
// It only constructs the official client (github.com/stripe/stripe-go).
// The services that call Stripe define small interfaces for exactly the
// calls they make (accept interfaces, defined where they are used), so they
// can be tested with fakes and never see the whole SDK.
package stripeclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v87"
)

// ErrInvalidKey: Stripe refused the secret key (wrong, rolled or revoked).
// A configuration error: the API should not start with it.
var ErrInvalidKey = errors.New("stripe: the secret key was refused (wrong, rolled or revoked?)")

// Config: how to reach Stripe.
type Config struct {
	SecretKey string
	// URL replaces https://api.stripe.com, for tests (an httptest server or
	// stripe-mock). Empty = the real Stripe API.
	URL string
	Log *slog.Logger
}

// New returns a Stripe client. Safe for concurrent use: create one in main
// and share it.
//
// The SDK retries network errors and 409/429/5xx responses twice (with
// backoff), adding an Idempotency-Key to POSTs so a retried "create" can
// never create twice.
func New(cfg Config) *stripe.Client {
	bc := &stripe.BackendConfig{
		// Every call is made inside one of our HTTP requests (30s write
		// timeout): never wait longer than that for Stripe.
		HTTPClient:        &http.Client{Timeout: 20 * time.Second},
		MaxNetworkRetries: stripe.Int64(2),
		LeveledLogger:     slogAdapter{log: cfg.Log.With("component", "stripe")},
	}
	if cfg.URL != "" {
		bc.URL = stripe.String(cfg.URL)
	}
	return stripe.NewClient(cfg.SecretKey, stripe.WithBackends(stripe.NewBackendsWithConfig(bc)))
}

// Check asks Stripe for the account the key belongs to: one cheap call
// that proves the key works. ErrInvalidKey means the key is wrong; any
// other error means Stripe could not be reached (network, outage).
func Check(ctx context.Context, c *stripe.Client) (*stripe.Account, error) {
	acct, err := c.V1Accounts.Retrieve(ctx, nil)
	if err != nil {
		var sErr *stripe.Error
		if errors.As(err, &sErr) && sErr.HTTPStatusCode == http.StatusUnauthorized {
			return nil, ErrInvalidKey
		}
		return nil, fmt.Errorf("stripe: retrieve account: %w", err)
	}
	return acct, nil
}

// slogAdapter routes the SDK's log lines into our structured logger. The SDK
// logs every request at "info" ("Requesting POST api.stripe.com/v1/..."),
// which is debug detail for us; its warnings and errors stay as they are.
type slogAdapter struct {
	log *slog.Logger
}

// Debugf drops the line: the SDK's debug level is the full body of every
// Stripe response (customer names, emails, addresses, card details), which
// must not reach our logs even when LOG_LEVEL=debug.
func (a slogAdapter) Debugf(context.Context, string, ...any) {}

func (a slogAdapter) Infof(ctx context.Context, format string, v ...any) {
	a.log.DebugContext(ctx, fmt.Sprintf(format, v...))
}

func (a slogAdapter) Warnf(ctx context.Context, format string, v ...any) {
	a.log.WarnContext(ctx, fmt.Sprintf(format, v...))
}

// Errorf logs at debug too: every Stripe error is also returned to our
// code, which decides whether it is a problem (a card id that does not
// exist is an expected 404, not an incident) and logs real failures itself.
func (a slogAdapter) Errorf(ctx context.Context, format string, v ...any) {
	a.log.DebugContext(ctx, fmt.Sprintf(format, v...))
}
