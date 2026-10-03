package repository

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// restoreRateLimit puts a rule back as it was when the test started: the
// rows are seed data shared by every test, so newTestDB does not delete them.
func restoreRateLimit(t *testing.T, db *pgxpool.Pool, name string) {
	t.Helper()
	repo := NewRateLimitRepository(db)
	orig, err := repo.GetByName(context.Background(), name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec(context.Background(),
			`UPDATE rate_limits SET max_requests = $2, window_seconds = $3, updated_by = NULL WHERE name = $1`,
			name, orig.Limit, int(orig.Window/time.Second))
		require.NoError(t, err)
	})
}

func TestRateLimitRepository_SeededRules(t *testing.T) {
	db := newTestDB(t)

	limits, err := NewRateLimitRepository(db).List(context.Background())

	require.NoError(t, err)
	require.Len(t, limits, 3)
	byName := map[string]model.RateLimit{}
	for _, rl := range limits {
		byName[rl.Name] = rl
	}
	// The values that used to be hard-coded in main.go.
	assert.Equal(t, 20, byName[model.RateLimitAuthRequests].Limit)
	assert.Equal(t, time.Minute, byName[model.RateLimitAuthRequests].Window)
	assert.Equal(t, 5, byName[model.RateLimitLoginAttempts].Limit)
	assert.Equal(t, 15*time.Minute, byName[model.RateLimitLoginAttempts].Window)
	assert.NotEmpty(t, byName[model.RateLimitLoginAttempts].Description)
	// Added with password reset (migration 000012).
	assert.Equal(t, 3, byName[model.RateLimitPasswordReset].Limit)
	assert.Equal(t, time.Hour, byName[model.RateLimitPasswordReset].Window)
}

func TestRateLimitRepository_GetByName_Unknown(t *testing.T) {
	db := newTestDB(t)

	_, err := NewRateLimitRepository(db).GetByName(context.Background(), "nope")

	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestRateLimitRepository_Update(t *testing.T) {
	db := newTestDB(t)
	restoreRateLimit(t, db, model.RateLimitLoginAttempts)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	admin, err := NewUserRepository(db).Create(context.Background(), newUser(company.ID, "a@acme.com"), []string{model.RoleAdmin})
	require.NoError(t, err)
	repo := NewRateLimitRepository(db)
	before, err := repo.GetByName(context.Background(), model.RateLimitLoginAttempts)
	require.NoError(t, err)

	updated, err := repo.Update(context.Background(), model.RateLimit{
		Name: model.RateLimitLoginAttempts, Limit: 3, Window: 10 * time.Minute, UpdatedBy: &admin.ID,
	})

	require.NoError(t, err)
	assert.Equal(t, 3, updated.Limit)
	assert.Equal(t, 10*time.Minute, updated.Window)
	require.NotNil(t, updated.UpdatedBy)
	assert.Equal(t, admin.ID, *updated.UpdatedBy)
	assert.Equal(t, before.Description, updated.Description, "description is not editable")
	assert.True(t, updated.UpdatedAt.After(before.UpdatedAt), "trigger bumps updated_at")

	got, err := repo.GetByName(context.Background(), model.RateLimitLoginAttempts)
	require.NoError(t, err)
	assert.Equal(t, updated.Limit, got.Limit, "persisted")
}

func TestRateLimitRepository_Update_Unknown(t *testing.T) {
	db := newTestDB(t)

	_, err := NewRateLimitRepository(db).Update(context.Background(),
		model.RateLimit{Name: "nope", Limit: 1, Window: time.Second})

	assert.ErrorIs(t, err, model.ErrNotFound)
}

// The CHECK constraints catch invalid values even if the service validation
// were bypassed.
func TestRateLimitRepository_Update_CheckConstraints(t *testing.T) {
	db := newTestDB(t)
	restoreRateLimit(t, db, model.RateLimitAuthRequests)
	repo := NewRateLimitRepository(db)

	for _, rl := range []model.RateLimit{
		{Limit: 0, Window: time.Minute},
		{Limit: 10001, Window: time.Minute},
		{Limit: 5, Window: 0},
		{Limit: 5, Window: 25 * time.Hour},
	} {
		rl.Name = model.RateLimitAuthRequests
		_, err := repo.Update(context.Background(), rl)
		assert.Error(t, err, "%+v must be rejected", rl)
	}
}
