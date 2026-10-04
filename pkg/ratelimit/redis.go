package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
)

// Redis counts in Redis, so every API instance shares one counter per key:
// with 3 servers, 5 login attempts means 5 in total, not 5 per server.
//
// The algorithm (GCRA, via redis_rate) behaves like Keyed's token bucket:
// `limit` events at once, then one more every window/limit. It runs as one
// atomic Lua script inside Redis and uses Redis' clock, so concurrent
// requests and clock differences between our servers cannot break it.
// Keys expire on their own once their bucket is full again.
//
// local is used twice:
//   - it holds the current limit and window (RateLimitService changes them
//     with SetLimit), read on every call;
//   - it is the fallback: when Redis cannot be reached, each instance counts
//     in its own memory, as before Redis. Logins keep working and stay
//     limited (less precisely) until Redis is back.
type Redis struct {
	limiter *redis_rate.Limiter
	prefix  string // e.g. "eld:login_attempts:" (redis_rate adds "rate:")
	local   *Keyed
	log     *slog.Logger

	degraded atomic.Bool // true while Redis is failing (logged once per outage)
}

// NewRedis returns a limiter for one rule. name separates the rules' keys in
// Redis.
func NewRedis(client redis.UniversalClient, name string, local *Keyed, log *slog.Logger) *Redis {
	return &Redis{
		limiter: redis_rate.NewLimiter(client),
		prefix:  "eld:" + name + ":",
		local:   local,
		log:     log.With("rate_limit", name),
	}
}

// Take uses one token for key; see Keyed.Take.
func (r *Redis) Take(ctx context.Context, key string) (retryAfter time.Duration, ok bool) {
	limit, window := r.local.Limit()
	res, err := r.limiter.Allow(ctx, r.redisKey(key), redis_rate.Limit{
		Rate: limit, Burst: limit, Period: window,
	})
	if err != nil {
		r.failed(err)
		return r.local.Take(ctx, key)
	}
	r.recovered()
	if res.Allowed == 0 {
		return res.RetryAfter, false
	}
	return 0, true
}

// Reset forgets key, in Redis and in the fallback.
func (r *Redis) Reset(ctx context.Context, key string) {
	r.local.Reset(ctx, key)
	if err := r.limiter.Reset(ctx, r.redisKey(key)); err != nil {
		r.failed(err)
		return
	}
	r.recovered()
}

// redisKey hashes key: login names must not be stored in Redis in clear
// text, and a hash bounds the key length whatever the client sends.
func (r *Redis) redisKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return r.prefix + hex.EncodeToString(sum[:])
}

// failed logs the first error of an outage only: during an outage every
// request fails, and one line per login would flood the logs.
func (r *Redis) failed(err error) {
	if r.degraded.CompareAndSwap(false, true) {
		r.log.Error("redis rate limiter unavailable, counting in memory per instance", "error", err)
	}
}

func (r *Redis) recovered() {
	if r.degraded.CompareAndSwap(true, false) {
		r.log.Info("redis rate limiter available again")
	}
}
