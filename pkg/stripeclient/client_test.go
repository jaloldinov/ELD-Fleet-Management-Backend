package stripeclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v87"
)

const fakeKey = "sk_test_fake"

// fakeStripe answers GET /v1/account with status and body, and records the
// request it got.
func fakeStripe(t *testing.T, status int, body string) (url string, got *http.Request) {
	t.Helper()
	got = &http.Request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = *r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, got
}

func newTestClient(url string) *stripe.Client {
	return New(Config{SecretKey: fakeKey, URL: url, Log: slog.New(slog.DiscardHandler)})
}

func TestCheck_OK(t *testing.T) {
	url, got := fakeStripe(t, http.StatusOK, `{"id":"acct_123","object":"account"}`)

	acct, err := Check(context.Background(), newTestClient(url))

	require.NoError(t, err)
	assert.Equal(t, "acct_123", acct.ID)
	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, "/v1/account", got.URL.Path)
	assert.Equal(t, "Bearer "+fakeKey, got.Header.Get("Authorization"))
	// The SDK pins the API version it was generated for: Stripe answers in
	// exactly the shape our structs expect, whatever the account default is.
	assert.Equal(t, stripe.APIVersion, got.Header.Get("Stripe-Version"))
}

func TestCheck_InvalidKey(t *testing.T) {
	url, _ := fakeStripe(t, http.StatusUnauthorized,
		`{"error":{"type":"invalid_request_error","message":"Invalid API Key provided"}}`)

	_, err := Check(context.Background(), newTestClient(url))

	assert.ErrorIs(t, err, ErrInvalidKey)
}

func TestCheck_OtherErrorIsNotInvalidKey(t *testing.T) {
	// 403 (e.g. a restricted key without account access) is not retried
	// and is not "wrong key": startup only warns.
	url, _ := fakeStripe(t, http.StatusForbidden,
		`{"error":{"type":"invalid_request_error","message":"not allowed"}}`)

	_, err := Check(context.Background(), newTestClient(url))

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrInvalidKey))
}

func TestLogs_NoResponseBodies(t *testing.T) {
	url, _ := fakeStripe(t, http.StatusOK, `{"id":"acct_123","object":"account","email":"owner@example.com"}`)
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := Check(context.Background(), New(Config{SecretKey: fakeKey, URL: url, Log: log}))

	require.NoError(t, err)
	assert.Contains(t, logs.String(), "Requesting GET", "request lines are kept, at debug level")
	assert.NotContains(t, logs.String(), "owner@example.com", "response bodies never reach the logs")
	assert.NotContains(t, logs.String(), fakeKey)
}
