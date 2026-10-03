package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type memRateLimitRepo struct {
	rows    map[string]model.RateLimit
	updated []model.RateLimit
	listErr error
}

func newMemRateLimitRepo() *memRateLimitRepo {
	return &memRateLimitRepo{rows: map[string]model.RateLimit{
		model.RateLimitAuthRequests:  {Name: model.RateLimitAuthRequests, Limit: 20, Window: time.Minute},
		model.RateLimitLoginAttempts: {Name: model.RateLimitLoginAttempts, Limit: 5, Window: 15 * time.Minute},
	}}
}

func (m *memRateLimitRepo) List(context.Context) ([]model.RateLimit, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]model.RateLimit, 0, len(m.rows))
	for _, rl := range m.rows {
		out = append(out, rl)
	}
	return out, nil
}

func (m *memRateLimitRepo) GetByName(_ context.Context, name string) (*model.RateLimit, error) {
	rl, ok := m.rows[name]
	if !ok {
		return nil, model.ErrNotFound
	}
	return &rl, nil
}

func (m *memRateLimitRepo) Update(_ context.Context, rl model.RateLimit) (*model.RateLimit, error) {
	if _, ok := m.rows[rl.Name]; !ok {
		return nil, model.ErrNotFound
	}
	m.updated = append(m.updated, rl)
	m.rows[rl.Name] = rl
	return &rl, nil
}

// takeAll counts how many attempts key gets before being refused.
func takeAll(l interface {
	Take(context.Context, string) (time.Duration, bool)
}, key string) int {
	n := 0
	for ; n < 1000; n++ {
		if _, ok := l.Take(context.Background(), key); !ok {
			break
		}
	}
	return n
}

func TestRateLimitService_LimiterUsesStoredValues(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())

	l, err := svc.Limiter(context.Background(), model.RateLimitLoginAttempts)

	require.NoError(t, err)
	limit, window := l.Limit()
	assert.Equal(t, 5, limit)
	assert.Equal(t, 15*time.Minute, window)
	assert.Equal(t, 5, takeAll(l, "jane|1.2.3.4"))
}

func TestRateLimitService_LimiterIsShared(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())

	a, err := svc.Limiter(context.Background(), model.RateLimitAuthRequests)
	require.NoError(t, err)
	b, err := svc.Limiter(context.Background(), model.RateLimitAuthRequests)
	require.NoError(t, err)

	assert.Same(t, a, b)
}

func TestRateLimitService_LimiterUnknownRule(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())

	_, err := svc.Limiter(context.Background(), "nope")

	assert.ErrorIs(t, err, model.ErrNotFound, "startup must fail if a rule is missing")
}

func TestRateLimitService_ListAndGet(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())

	list, err := svc.List(context.Background(), platformAdmin)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	rl, err := svc.Get(context.Background(), platformAdmin, model.RateLimitAuthRequests)
	require.NoError(t, err)
	assert.Equal(t, 20, rl.Limit)

	_, err = svc.Get(context.Background(), platformAdmin, "nope")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestRateLimitService_CompanyUsersAreForbidden(t *testing.T) {
	repo := newMemRateLimitRepo()
	svc := NewRateLimitService(repo)

	_, err := svc.List(context.Background(), companyAUser)
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = svc.Get(context.Background(), companyAUser, model.RateLimitAuthRequests)
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = svc.Update(context.Background(), companyAUser, model.RateLimitAuthRequests,
		UpdateRateLimitInput{Limit: ptr(1000)})
	assert.ErrorIs(t, err, model.ErrForbidden)

	assert.Empty(t, repo.updated, "nothing saved")
}

func TestRateLimitService_UpdateSavesAndAppliesAtOnce(t *testing.T) {
	repo := newMemRateLimitRepo()
	svc := NewRateLimitService(repo)
	l, err := svc.Limiter(context.Background(), model.RateLimitLoginAttempts)
	require.NoError(t, err)

	rl, err := svc.Update(context.Background(), platformAdmin, model.RateLimitLoginAttempts,
		UpdateRateLimitInput{Limit: ptr(3), WindowSeconds: ptr(600)})

	require.NoError(t, err)
	assert.Equal(t, 3, rl.Limit)
	assert.Equal(t, 10*time.Minute, rl.Window)
	require.Len(t, repo.updated, 1, "saved to the database")
	require.NotNil(t, repo.updated[0].UpdatedBy)
	assert.Equal(t, platformAdmin.UserID, *repo.updated[0].UpdatedBy, "records who changed it")

	limit, window := l.Limit()
	assert.Equal(t, 3, limit, "the running limiter changed without a restart")
	assert.Equal(t, 10*time.Minute, window)
	assert.Equal(t, 3, takeAll(l, "jane|1.2.3.4"))
}

func TestRateLimitService_UpdatePartial(t *testing.T) {
	repo := newMemRateLimitRepo()
	svc := NewRateLimitService(repo)

	rl, err := svc.Update(context.Background(), platformAdmin, model.RateLimitAuthRequests,
		UpdateRateLimitInput{Limit: ptr(50)})

	require.NoError(t, err)
	assert.Equal(t, 50, rl.Limit)
	assert.Equal(t, time.Minute, rl.Window, "window unchanged")
}

func TestRateLimitService_RulesAreIndependent(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())
	authL, err := svc.Limiter(context.Background(), model.RateLimitAuthRequests)
	require.NoError(t, err)
	loginL, err := svc.Limiter(context.Background(), model.RateLimitLoginAttempts)
	require.NoError(t, err)

	_, err = svc.Update(context.Background(), platformAdmin, model.RateLimitLoginAttempts,
		UpdateRateLimitInput{Limit: ptr(2)})
	require.NoError(t, err)

	limit, window := authL.Limit()
	assert.Equal(t, 20, limit, "auth_requests untouched")
	assert.Equal(t, time.Minute, window)
	limit, _ = loginL.Limit()
	assert.Equal(t, 2, limit)
}

func TestRateLimitService_UpdateValidation(t *testing.T) {
	tests := []struct {
		name  string
		in    UpdateRateLimitInput
		field string
	}{
		{"empty body", UpdateRateLimitInput{}, "body"},
		{"zero limit", UpdateRateLimitInput{Limit: ptr(0)}, "limit"},
		{"negative limit", UpdateRateLimitInput{Limit: ptr(-5)}, "limit"},
		{"limit too high", UpdateRateLimitInput{Limit: ptr(10001)}, "limit"},
		{"zero window", UpdateRateLimitInput{WindowSeconds: ptr(0)}, "window_seconds"},
		{"negative window", UpdateRateLimitInput{WindowSeconds: ptr(-60)}, "window_seconds"},
		{"window over 24h", UpdateRateLimitInput{WindowSeconds: ptr(86401)}, "window_seconds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMemRateLimitRepo()
			svc := NewRateLimitService(repo)

			_, err := svc.Update(context.Background(), platformAdmin, model.RateLimitLoginAttempts, tc.in)

			requireValidationField(t, err, tc.field)
			assert.Empty(t, repo.updated, "nothing saved")
		})
	}
}

func TestRateLimitService_UpdateBoundaries(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())

	_, err := svc.Update(context.Background(), platformAdmin, model.RateLimitLoginAttempts,
		UpdateRateLimitInput{Limit: ptr(1), WindowSeconds: ptr(1)})
	require.NoError(t, err)

	_, err = svc.Update(context.Background(), platformAdmin, model.RateLimitLoginAttempts,
		UpdateRateLimitInput{Limit: ptr(10000), WindowSeconds: ptr(86400)})
	require.NoError(t, err)
}

func TestRateLimitService_UpdateUnknownRule(t *testing.T) {
	svc := NewRateLimitService(newMemRateLimitRepo())

	_, err := svc.Update(context.Background(), platformAdmin, "nope", UpdateRateLimitInput{Limit: ptr(5)})

	assert.ErrorIs(t, err, model.ErrNotFound)
}

// Another instance (or someone in psql) changed the row: Refresh picks it up.
func TestRateLimitService_RefreshAppliesStoredChanges(t *testing.T) {
	repo := newMemRateLimitRepo()
	svc := NewRateLimitService(repo)
	l, err := svc.Limiter(context.Background(), model.RateLimitAuthRequests)
	require.NoError(t, err)

	repo.rows[model.RateLimitAuthRequests] = model.RateLimit{Name: model.RateLimitAuthRequests, Limit: 100, Window: time.Hour}
	require.NoError(t, svc.Refresh(context.Background()))

	limit, window := l.Limit()
	assert.Equal(t, 100, limit)
	assert.Equal(t, time.Hour, window)
}

func TestRateLimitService_RefreshKeepsValuesOnError(t *testing.T) {
	repo := newMemRateLimitRepo()
	svc := NewRateLimitService(repo)
	l, err := svc.Limiter(context.Background(), model.RateLimitAuthRequests)
	require.NoError(t, err)

	repo.listErr = errors.New("db down")
	require.Error(t, svc.Refresh(context.Background()))

	limit, _ := l.Limit()
	assert.Equal(t, 20, limit, "a database outage must not change or disable the limits")
}
