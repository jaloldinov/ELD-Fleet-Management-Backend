// Package jobs runs periodic background work inside the API process.
package jobs

import (
	"context"
	"log/slog"
	"time"
)

// Every runs fn once right away, then every interval, until ctx is
// cancelled. A failed run is logged and retried at the next tick: background
// work must never crash the API. A panic in fn is not recovered, on
// purpose: it is a bug, and crashing loudly beats running half-broken.
//
// With several API instances, each one runs the job. Jobs must therefore be
// safe to run concurrently (idempotent), like deleting expired rows.
func Every(ctx context.Context, log *slog.Logger, name string, interval time.Duration, fn func(context.Context) error) {
	log = log.With("job", name)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		start := time.Now()
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			log.Error("job failed", "error", err, "duration_ms", time.Since(start).Milliseconds())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
