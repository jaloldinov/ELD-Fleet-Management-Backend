package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLimiter returns a limiter with a controllable clock.
func newTestLimiter(limit int, window time.Duration) (*Keyed, *time.Time) {
	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	k := New(limit, window)
	k.now = func() time.Time { return clock }
	return k, &clock
}

func TestKeyed_BurstThenBlocked(t *testing.T) {
	k, _ := newTestLimiter(5, 15*time.Minute)

	for i := range 5 {
		_, ok := k.Take("a")
		assert.True(t, ok, "attempt %d is within the burst", i+1)
	}
	retry, ok := k.Take("a")

	assert.False(t, ok)
	assert.Equal(t, 3*time.Minute, retry, "one token comes back every 15m/5")
}

func TestKeyed_KeysAreIndependent(t *testing.T) {
	k, _ := newTestLimiter(1, time.Minute)
	k.Take("a")

	_, ok := k.Take("b")

	assert.True(t, ok)
}

func TestKeyed_TokensComeBack(t *testing.T) {
	k, clock := newTestLimiter(5, 15*time.Minute)
	for range 5 {
		k.Take("a")
	}

	*clock = clock.Add(3 * time.Minute)
	_, ok := k.Take("a")
	assert.True(t, ok, "one token after 3 minutes")

	_, ok = k.Take("a")
	assert.False(t, ok, "but only one")
}

func TestKeyed_RefusedAttemptsDoNotExtendTheWait(t *testing.T) {
	k, clock := newTestLimiter(1, time.Minute)
	k.Take("a")
	for range 10 {
		k.Take("a") // hammering while blocked
	}

	*clock = clock.Add(time.Minute)
	_, ok := k.Take("a")

	assert.True(t, ok, "refused attempts were cancelled, not queued")
}

func TestKeyed_Reset(t *testing.T) {
	k, _ := newTestLimiter(1, time.Hour)
	k.Take("a")

	k.Reset("a")
	_, ok := k.Take("a")

	assert.True(t, ok)
}

func TestKeyed_SweepRemovesOnlyIdleKeys(t *testing.T) {
	k, clock := newTestLimiter(5, time.Minute)
	k.Take("old")
	*clock = clock.Add(50 * time.Second)
	k.Take("recent")

	*clock = clock.Add(20 * time.Second) // "old" idle 70s, "recent" 20s
	k.sweep()

	assert.Equal(t, 1, k.Len())
	k.mu.Lock()
	_, kept := k.buckets["recent"]
	k.mu.Unlock()
	assert.True(t, kept)
}

func TestKeyed_RunCleanupStopsWithContext(t *testing.T) {
	k := New(1, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.RunCleanup(ctx); close(done) }()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunCleanup did not stop")
	}
}

func TestNew_PanicsOnInvalidLimit(t *testing.T) {
	assert.Panics(t, func() { New(0, time.Minute) })
	assert.Panics(t, func() { New(1, 0) })
}

func TestKeyed_SetLimitAppliesToNewKeys(t *testing.T) {
	k, _ := newTestLimiter(5, 15*time.Minute)

	require.NoError(t, k.SetLimit(2, time.Minute))
	limit, window := k.Limit()
	assert.Equal(t, 2, limit)
	assert.Equal(t, time.Minute, window)

	k.Take("a")
	k.Take("a")
	retry, ok := k.Take("a")
	assert.False(t, ok, "the new limit (2) is used")
	assert.Equal(t, 30*time.Second, retry, "one token comes back every 1m/2")
}

func TestKeyed_SetLimitCapsRemainingTokens(t *testing.T) {
	k, _ := newTestLimiter(5, 15*time.Minute)
	k.Take("a") // 4 of 5 left

	require.NoError(t, k.SetLimit(2, 15*time.Minute))

	for i := range 2 {
		_, ok := k.Take("a")
		assert.True(t, ok, "attempt %d: 4 left, capped at the new limit 2", i+1)
	}
	_, ok := k.Take("a")
	assert.False(t, ok)
}

func TestKeyed_SetLimitDoesNotRefillExhaustedKeys(t *testing.T) {
	k, _ := newTestLimiter(5, 15*time.Minute)
	for range 5 {
		k.Take("a") // exhausted
	}

	require.NoError(t, k.SetLimit(10, 15*time.Minute))
	_, ok := k.Take("a")

	assert.False(t, ok, "saving settings must not give attackers fresh attempts")
}

func TestKeyed_SetLimitLooseningRefillsAtNewRate(t *testing.T) {
	k, clock := newTestLimiter(1, time.Hour)
	k.Take("a") // blocked for an hour at the old rate

	require.NoError(t, k.SetLimit(60, time.Hour)) // one token per minute
	*clock = clock.Add(time.Minute)
	_, ok := k.Take("a")

	assert.True(t, ok, "tokens come back at the new rate")
}

func TestKeyed_SetLimitRejectsInvalidValues(t *testing.T) {
	k, _ := newTestLimiter(5, time.Minute)

	assert.ErrorIs(t, k.SetLimit(0, time.Minute), ErrInvalidLimit)
	assert.ErrorIs(t, k.SetLimit(5, 0), ErrInvalidLimit)
	limit, window := k.Limit()
	assert.Equal(t, 5, limit, "unchanged")
	assert.Equal(t, time.Minute, window, "unchanged")
}

func TestKeyed_SweepUsesCurrentWindow(t *testing.T) {
	k, clock := newTestLimiter(5, time.Hour)
	k.Take("a")
	require.NoError(t, k.SetLimit(5, time.Minute))

	*clock = clock.Add(2 * time.Minute)
	k.sweep()

	assert.Equal(t, 0, k.Len(), "idle for longer than the new window")
}
