package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func newResetFixture(t *testing.T) (*pgxpool.Pool, *PasswordResetRepository, uuid.UUID) {
	t.Helper()
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	u, err := NewUserRepository(db).Create(context.Background(), newUser(company.ID, "jane@acme.com"), []string{model.RoleDispatcher})
	require.NoError(t, err)
	return db, NewPasswordResetRepository(db), u.ID
}

func TestPasswordReset_ReplaceAndGetPending(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	ctx := context.Background()

	require.NoError(t, repo.Replace(ctx, userID, []byte("first"), 10*time.Minute))
	require.NoError(t, repo.Replace(ctx, userID, []byte("second"), 10*time.Minute))

	pr, err := repo.GetPending(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, []byte("second"), pr.CodeHash, "a new request replaces the old code")
	assert.Equal(t, 0, pr.Attempts)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), pr.CodeExpiresAt, time.Minute)
}

func TestPasswordReset_ExpiredCodeIsNotPending(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	require.NoError(t, repo.Replace(context.Background(), userID, []byte("h"), -time.Second))

	_, err := repo.GetPending(context.Background(), userID)

	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestPasswordReset_AttemptsRunOut(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, userID, []byte("h"), 10*time.Minute))
	pr, err := repo.GetPending(ctx, userID)
	require.NoError(t, err)

	for i := range 5 {
		require.NoError(t, repo.ConsumeAttempt(ctx, pr.ID, 5), "attempt %d", i+1)
	}

	assert.ErrorIs(t, repo.ConsumeAttempt(ctx, pr.ID, 5), model.ErrNotFound, "the 6th is refused")
}

// 20 parallel guesses must get exactly 5 attempts, not more.
func TestPasswordReset_AttemptsAreAtomic(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, userID, []byte("h"), 10*time.Minute))
	pr, err := repo.GetPending(ctx, userID)
	require.NoError(t, err)

	var ok atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if repo.ConsumeAttempt(ctx, pr.ID, 5) == nil {
				ok.Add(1)
			}
		})
	}
	wg.Wait()

	assert.Equal(t, int32(5), ok.Load())
}

func TestPasswordReset_VerifyThenUseTokenOnce(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, userID, []byte("h"), 10*time.Minute))
	pr, err := repo.GetPending(ctx, userID)
	require.NoError(t, err)

	require.NoError(t, repo.MarkVerified(ctx, pr.ID, []byte("token-hash"), 10*time.Minute))
	assert.ErrorIs(t, repo.MarkVerified(ctx, pr.ID, []byte("other"), 10*time.Minute), model.ErrNotFound,
		"a code is verified once")
	_, err = repo.GetPending(ctx, userID)
	assert.ErrorIs(t, err, model.ErrNotFound, "a verified code is no longer pending")

	got, err := repo.UseToken(ctx, []byte("token-hash"))
	require.NoError(t, err)
	assert.Equal(t, userID, got)

	_, err = repo.UseToken(ctx, []byte("token-hash"))
	assert.ErrorIs(t, err, model.ErrNotFound, "a token works once")
}

func TestPasswordReset_ExpiredOrUnknownToken(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, userID, []byte("h"), 10*time.Minute))
	pr, err := repo.GetPending(ctx, userID)
	require.NoError(t, err)
	require.NoError(t, repo.MarkVerified(ctx, pr.ID, []byte("expired"), -time.Second))

	_, err = repo.UseToken(ctx, []byte("expired"))
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = repo.UseToken(ctx, []byte("unknown"))
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestPasswordReset_DeleteCreatedBefore(t *testing.T) {
	_, repo, userID := newResetFixture(t)
	ctx := context.Background()
	require.NoError(t, repo.Replace(ctx, userID, []byte("h"), 10*time.Minute))

	n, err := repo.DeleteCreatedBefore(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "recent rows stay")

	n, err = repo.DeleteCreatedBefore(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestUserRepository_UpdatePassword(t *testing.T) {
	db, _, userID := newResetFixture(t)
	users := NewUserRepository(db)
	ctx := context.Background()

	require.NoError(t, users.UpdatePassword(ctx, userID, "$2a$04$newhash"))
	u, err := users.GetByID(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, "$2a$04$newhash", u.PasswordHash)

	_, err = db.Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, userID)
	require.NoError(t, err)
	assert.ErrorIs(t, users.UpdatePassword(ctx, userID, "$2a$04$other"), model.ErrNotFound, "not for disabled users")
}
