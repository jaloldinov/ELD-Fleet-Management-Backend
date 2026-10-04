// Package ratelimit limits how often something may happen per key (a client
// IP, a login...), using one token bucket per key.
//
// State lives in this process's memory. That is right for a single
// instance; with several instances behind a load balancer each one counts
// separately, and the limiter should move to a shared store (e.g. Redis)
// behind the same methods.
package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Keyed allows `limit` events per `window` for each key. Up to `limit`
// events may happen at once (the burst); after that, tokens come back
// evenly over the window. The numbers can be changed while it runs
// (SetLimit).
type Keyed struct {
	now func() time.Time // replaceable in tests

	mu      sync.Mutex
	limit   int
	window  time.Duration
	every   rate.Limit
	buckets map[string]*bucket
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// ErrInvalidLimit: limit and window must both be positive.
var ErrInvalidLimit = errors.New("ratelimit: limit and window must be positive")

// New panics on a non-positive limit or window: that is a programming
// error, and validated input never produces it.
func New(limit int, window time.Duration) *Keyed {
	if limit < 1 || window <= 0 {
		panic(ErrInvalidLimit)
	}
	return &Keyed{
		limit:   limit,
		window:  window,
		every:   everyFor(limit, window),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

func everyFor(limit int, window time.Duration) rate.Limit {
	return rate.Every(window / time.Duration(limit))
}

// SetLimit changes the limit and window for every key, including keys that
// already have a bucket. Each bucket keeps its remaining tokens, capped at
// the new limit: tightening takes effect at once, and an exhausted key stays
// exhausted, its tokens coming back at the new rate. (Clearing the buckets
// instead would hand every attacker a fresh set of attempts each time an
// admin saves the settings.)
func (k *Keyed) SetLimit(limit int, window time.Duration) error {
	if limit < 1 || window <= 0 {
		return ErrInvalidLimit
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if limit == k.limit && window == k.window {
		return nil // nothing changed (the periodic refresh usually ends here)
	}

	k.limit, k.window, k.every = limit, window, everyFor(limit, window)
	now := k.now()
	for _, b := range k.buckets {
		b.lim.SetLimitAt(now, k.every)
		b.lim.SetBurstAt(now, k.limit)
	}
	return nil
}

// Limit returns the current limit and window.
func (k *Keyed) Limit() (limit int, window time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.limit, k.window
}

// Take uses one token for key. When none is left, ok is false and
// retryAfter says when the next token arrives.
func (k *Keyed) Take(_ context.Context, key string) (retryAfter time.Duration, ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	now := k.now()
	b, exists := k.buckets[key]
	if !exists {
		b = &bucket{lim: rate.NewLimiter(k.every, k.limit)}
		k.buckets[key] = b
	}
	b.lastSeen = now

	r := b.lim.ReserveN(now, 1)
	if delay := r.DelayFrom(now); delay > 0 {
		r.CancelAt(now) // we do not wait for it: give the reservation back
		return delay, false
	}
	return 0, true
}

// Reset forgets key, giving it a full bucket again (e.g. after a successful
// login, earlier failures no longer count).
func (k *Keyed) Reset(_ context.Context, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.buckets, key)
}

// sweep removes buckets unused for a whole window. Their bucket is full
// again by then, so deleting them changes nothing except memory use.
func (k *Keyed) sweep() {
	k.mu.Lock()
	defer k.mu.Unlock()
	cutoff := k.now().Add(-k.window)
	for key, b := range k.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(k.buckets, key)
		}
	}
}

// Len returns the number of tracked keys (for tests and metrics).
func (k *Keyed) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.buckets)
}

// RunCleanup sweeps idle keys every window until ctx is cancelled. Without
// it, every IP that ever called us would stay in memory forever.
// The window is read again after each sweep, so SetLimit also changes how
// often it runs.
func (k *Keyed) RunCleanup(ctx context.Context) {
	_, window := k.Limit()
	t := time.NewTimer(window)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			k.sweep()
			_, window = k.Limit()
			t.Reset(window)
		}
	}
}
