package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
)

// RateLimiter is a per-key limiter (ratelimit.Keyed or ratelimit.Redis).
type RateLimiter interface {
	Take(ctx context.Context, key string) (retryAfter time.Duration, ok bool)
}

// RateLimitByIP refuses requests with 429 once a client IP is over its limit.
// c.ClientIP() is the TCP peer address, because the router trusts no proxy
// headers; behind a load balancer, configure trusted proxies first, or every
// client will share the balancer's IP (and its limit).
func RateLimitByIP(l RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if retryAfter, ok := l.Take(c.Request.Context(), c.ClientIP()); !ok {
			apierror.RespondRateLimited(c, retryAfter)
			return
		}
		c.Next()
	}
}
