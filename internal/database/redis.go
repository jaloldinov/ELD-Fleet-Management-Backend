package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedis creates a Redis client from a URL like redis://host:6379/0
// (rediss:// for TLS).
//
// Unlike NewPool it does not ping: Redis only backs the rate limiters, which
// fall back to in-memory counting when Redis is unreachable, so the API
// starts (and keeps running) without it. The caller pings and logs.
//
// The defaults are tuned to fail FAST: while Redis is broken, every login
// should switch to the fallback within a fraction of a second instead of
// waiting through go-redis' default 5s timeouts and retries.
//
// It also routes go-redis' internal messages (one plain-text line on stderr
// per failed dial) into log at debug level: the rate limiter already logs an
// outage once, in our structured format. go-redis' logger is global, so the
// last call wins; the API creates one client.
func NewRedis(redisURL string, log *slog.Logger) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		// Do not include the URL in the error: it may contain the password.
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	opts.DialTimeout = 500 * time.Millisecond
	opts.ReadTimeout = 300 * time.Millisecond
	opts.WriteTimeout = 300 * time.Millisecond
	opts.PoolTimeout = 500 * time.Millisecond
	opts.MaxRetries = -1   // no command retries: the fallback is the retry
	opts.DialerRetries = 1 // one dial attempt per request, no retry loop
	redis.SetLogger(redisLogger{log: log.With("component", "go-redis")})
	return redis.NewClient(opts), nil
}

// redisLogger adapts slog to go-redis' logging interface.
type redisLogger struct {
	log *slog.Logger
}

func (l redisLogger) Printf(ctx context.Context, format string, v ...any) {
	l.log.DebugContext(ctx, fmt.Sprintf(format, v...))
}
