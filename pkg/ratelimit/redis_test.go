package ratelimit

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var bg = context.Background()

// newMiniRedis starts an in-process Redis with a controllable clock (the
// GCRA script reads Redis' TIME, which miniredis takes from SetTime).
func newMiniRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialerRetries: 1})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

// newServer is one API instance: its own local limiter, the shared Redis.
func newServer(client redis.UniversalClient, limit int, window time.Duration, log *bytes.Buffer) *Redis {
	if log == nil {
		log = &bytes.Buffer{}
	}
	return NewRedis(client, "login_attempts", New(limit, window), slog.New(slog.NewTextHandler(log, nil)))
}

// The reason for Redis: 3 servers must share one limit, not get 5 each.
func TestRedis_ServersShareOneCounter(t *testing.T) {
	_, client := newMiniRedis(t)
	a := newServer(client, 5, 15*time.Minute, nil)
	b := newServer(client, 5, 15*time.Minute, nil)
	c := newServer(client, 5, 15*time.Minute, nil)

	for _, s := range []*Redis{a, b, c, a, b} { // 5 attempts spread over 3 servers
		_, ok := s.Take(bg, "jane|1.2.3.4")
		require.True(t, ok)
	}

	for name, s := range map[string]*Redis{"a": a, "b": b, "c": c} {
		retry, ok := s.Take(bg, "jane|1.2.3.4")
		assert.False(t, ok, "server %s: the 6th attempt is refused everywhere", name)
		assert.InDelta(t, 3*time.Minute, retry, float64(time.Second), "one token comes back every 15m/5")
	}
}

func TestRedis_KeysAreIndependent(t *testing.T) {
	_, client := newMiniRedis(t)
	s := newServer(client, 1, time.Minute, nil)
	s.Take(bg, "a")

	_, ok := s.Take(bg, "b")

	assert.True(t, ok)
}

func TestRedis_TokensComeBack(t *testing.T) {
	mr, client := newMiniRedis(t)
	s := newServer(client, 5, 15*time.Minute, nil)
	for range 5 {
		s.Take(bg, "a")
	}

	mr.SetTime(time.Date(2026, 10, 3, 12, 3, 0, 0, time.UTC)) // +3 minutes
	_, ok := s.Take(bg, "a")
	assert.True(t, ok, "one token after 3 minutes")

	_, ok = s.Take(bg, "a")
	assert.False(t, ok, "but only one")
}

func TestRedis_RefusedAttemptsDoNotExtendTheWait(t *testing.T) {
	mr, client := newMiniRedis(t)
	s := newServer(client, 1, time.Minute, nil)
	s.Take(bg, "a")
	for range 10 {
		s.Take(bg, "a") // hammering while blocked
	}

	mr.SetTime(time.Date(2026, 10, 3, 12, 1, 0, 0, time.UTC))
	_, ok := s.Take(bg, "a")

	assert.True(t, ok)
}

func TestRedis_Reset(t *testing.T) {
	_, client := newMiniRedis(t)
	a := newServer(client, 1, time.Hour, nil)
	b := newServer(client, 1, time.Hour, nil)
	a.Take(bg, "a")

	a.Reset(bg, "a") // e.g. successful login on server a
	_, ok := b.Take(bg, "a")

	assert.True(t, ok, "the reset is visible to every server")
}

// The limit comes from the rate-limit settings (RateLimitService calls
// SetLimit on the local limiter); Redis uses the current value per call.
func TestRedis_UsesCurrentLimit(t *testing.T) {
	_, client := newMiniRedis(t)
	s := newServer(client, 5, 15*time.Minute, nil)

	require.NoError(t, s.local.SetLimit(2, 15*time.Minute))

	s.Take(bg, "a")
	s.Take(bg, "a")
	_, ok := s.Take(bg, "a")
	assert.False(t, ok, "the third attempt is over the new limit of 2")
}

func TestRedis_KeysAreHashedAndExpire(t *testing.T) {
	mr, client := newMiniRedis(t)
	s := newServer(client, 5, 15*time.Minute, nil)
	s.Take(bg, "jane@acme.com|1.2.3.4")

	keys := mr.Keys()
	require.Len(t, keys, 1)
	assert.True(t, strings.HasPrefix(keys[0], "rate:eld:login_attempts:"))
	assert.NotContains(t, keys[0], "jane", "no login names in clear text")
	assert.Greater(t, mr.TTL(keys[0]), time.Duration(0), "the key expires on its own")

	mr.FastForward(4 * time.Minute) // longer than the 3 minutes one token takes
	assert.Empty(t, mr.Keys(), "no cleanup job needed")
}

func TestRedis_FallsBackToMemoryWhenRedisIsDown(t *testing.T) {
	mr, client := newMiniRedis(t)
	var log bytes.Buffer
	s := newServer(client, 5, 15*time.Minute, &log)

	mr.Close()

	for i := range 5 {
		_, ok := s.Take(bg, "a")
		assert.True(t, ok, "attempt %d: logins keep working", i+1)
	}
	_, ok := s.Take(bg, "a")
	assert.False(t, ok, "and stay limited, by the local counter")
	s.Reset(bg, "a")
	assert.Equal(t, 1, strings.Count(log.String(), "redis rate limiter unavailable"),
		"logged once per outage, not once per request")

	require.NoError(t, mr.Restart())
	_, ok = s.Take(bg, "b")
	assert.True(t, ok)
	assert.Contains(t, log.String(), "redis rate limiter available again")
}

// Runs the shared-counter check against a real Redis when TEST_REDIS_URL is
// set (make test-integration, CI): miniredis runs Lua with a different
// interpreter, so the real thing is checked too.
func TestRedis_RealServer(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set; run `make test-integration`")
	}
	opts, err := redis.ParseURL(url)
	require.NoError(t, err)
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(bg).Err())

	a := newServer(client, 3, time.Minute, nil)
	b := newServer(client, 3, time.Minute, nil)
	key := "test|" + uuid.NewString() // unique: no flushing needed
	t.Cleanup(func() { a.Reset(bg, key) })

	for _, s := range []*Redis{a, b, a} {
		_, ok := s.Take(bg, key)
		require.True(t, ok)
	}
	retry, ok := b.Take(bg, key)

	assert.False(t, ok)
	assert.InDelta(t, 20*time.Second, retry, float64(time.Second))
	assert.False(t, a.degraded.Load(), "no fallback happened")
}
