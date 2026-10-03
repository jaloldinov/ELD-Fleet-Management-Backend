package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/ratelimit"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// RateLimitRepository is what RateLimitService needs from the data layer.
type RateLimitRepository interface {
	List(ctx context.Context) ([]model.RateLimit, error)
	GetByName(ctx context.Context, name string) (*model.RateLimit, error)
	Update(ctx context.Context, rl model.RateLimit) (*model.RateLimit, error)
}

// Allowed ranges (same as the CHECK constraints on rate_limits).
const (
	rateLimitMax       = 10000
	rateLimitWindowMax = 24 * time.Hour
)

// UpdateRateLimitInput: nil = unchanged.
type UpdateRateLimitInput struct {
	Limit         *int
	WindowSeconds *int
}

// RateLimitService manages the rate-limit settings (a platform setting) and
// keeps the in-memory limiters in step with the database.
//
// How the values reach the limiters, without a database query per request:
//   - startup: main gets each limiter from Limiter(), built from the stored row;
//   - Update: saves the row, then changes the limiter at once;
//   - Refresh (every minute): re-reads all rows, so changes made through
//     another instance (or directly in the database) arrive here too.
type RateLimitService struct {
	repo RateLimitRepository

	mu       sync.Mutex
	limiters map[string]*ratelimit.Keyed // by rule name
}

func NewRateLimitService(repo RateLimitRepository) *RateLimitService {
	return &RateLimitService{repo: repo, limiters: make(map[string]*ratelimit.Keyed)}
}

// Limiter returns the limiter for rule name, built from its stored settings.
// Later changes to the rule are applied to this same limiter. Calling it
// again for the same name returns the same limiter.
func (s *RateLimitService) Limiter(ctx context.Context, name string) (*ratelimit.Keyed, error) {
	rl, err := s.repo.GetByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("load rate limit %q: %w", name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.limiters[name]; ok {
		return l, l.SetLimit(rl.Limit, rl.Window)
	}
	if rl.Limit < 1 || rl.Window <= 0 {
		return nil, fmt.Errorf("rate limit %q: stored values are invalid", name)
	}
	l := ratelimit.New(rl.Limit, rl.Window)
	s.limiters[name] = l
	return l, nil
}

// Refresh re-reads every rule and applies it to its limiter. One cheap
// query; limiters whose values did not change are left alone.
func (s *RateLimitService) Refresh(ctx context.Context) error {
	limits, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, rl := range limits {
		if err := s.apply(rl); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// List, Get and Update are for platform users only. The permissions
// (ratelimit:read/update) are already seeded for platform_admin only; this
// is defense in depth, as for companies: a carrier must never be able to
// change limits that protect every carrier.

func (s *RateLimitService) List(ctx context.Context, p auth.Principal) ([]model.RateLimit, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	return s.repo.List(ctx)
}

func (s *RateLimitService) Get(ctx context.Context, p auth.Principal, name string) (*model.RateLimit, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	return s.repo.GetByName(ctx, name)
}

func (s *RateLimitService) Update(ctx context.Context, p auth.Principal, name string, in UpdateRateLimitInput) (*model.RateLimit, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	if in == (UpdateRateLimitInput{}) {
		return nil, &validation.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	// Validate the raw input first: a huge window_seconds must not overflow
	// when converted to a time.Duration.
	var v validation.Validator
	if in.Limit != nil {
		v.Check(*in.Limit >= 1 && *in.Limit <= rateLimitMax, "limit", "must be between 1 and 10000")
	}
	if in.WindowSeconds != nil {
		v.Check(*in.WindowSeconds >= 1 && *in.WindowSeconds <= int(rateLimitWindowMax/time.Second),
			"window_seconds", "must be between 1 and 86400 (24 hours)")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}

	rl, err := s.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if in.Limit != nil {
		rl.Limit = *in.Limit
	}
	if in.WindowSeconds != nil {
		rl.Window = time.Duration(*in.WindowSeconds) * time.Second
	}
	rl.UpdatedBy = &p.UserID

	saved, err := s.repo.Update(ctx, *rl)
	if err != nil {
		return nil, err
	}
	// Saved first, applied second: if the process dies in between, the next
	// start (or Refresh) still applies the saved values.
	if err := s.apply(*saved); err != nil {
		return nil, err
	}
	return saved, nil
}

// apply sets rl on its limiter, if this process has one for that rule.
func (s *RateLimitService) apply(rl model.RateLimit) error {
	s.mu.Lock()
	l, ok := s.limiters[rl.Name]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	if err := l.SetLimit(rl.Limit, rl.Window); err != nil {
		return fmt.Errorf("apply rate limit %q: %w", rl.Name, err)
	}
	return nil
}
