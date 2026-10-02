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
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Keyed allows `limit` events per `window` for each key. Up to `limit`
// events may happen at once (the burst); after that, tokens come back
// evenly over the window.
type Keyed struct {
	limit  int
	window time.Duration
	every  rate.Limit
	now    func() time.Time // replaceable in tests

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

func New(limit int, window time.Duration) *Keyed {
	return &Keyed{
		limit:   limit,
		window:  window,
		every:   rate.Every(window / time.Duration(limit)),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// Take uses one token for key. When none is left, ok is false and
// retryAfter says when the next token arrives.
func (k *Keyed) Take(key string) (retryAfter time.Duration, ok bool) {
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
func (k *Keyed) Reset(key string) {
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
func (k *Keyed) RunCleanup(ctx context.Context) {
	t := time.NewTicker(k.window)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			k.sweep()
		}
	}
}
