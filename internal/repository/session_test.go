package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// sessionFixture: a database with one user, ready for session tests.
type sessionFixture struct {
	db     *pgxpool.Pool
	repo   *SessionRepository
	userID uuid.UUID
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	db := newTestDB(t)
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	u, err := NewUserRepository(db).Create(context.Background(),
		newUser(company.ID, "jane@acme.com"), []string{model.RoleDriver})
	require.NoError(t, err)
	return &sessionFixture{db: db, repo: NewSessionRepository(db), userID: u.ID}
}

// newToken returns a plain token and an unsaved model for it.
func (f *sessionFixture) newToken(t *testing.T, expiresIn time.Duration) (string, model.RefreshToken) {
	t.Helper()
	plain, hash, err := auth.NewRefreshToken()
	require.NoError(t, err)
	return plain, model.RefreshToken{
		UserID: f.userID, TokenHash: hash, ExpiresAt: time.Now().Add(expiresIn),
		UserAgent: "test-agent", IP: "127.0.0.1",
	}
}

// login stores a new session and returns its plain token.
func (f *sessionFixture) login(t *testing.T) string {
	t.Helper()
	plain, tok := f.newToken(t, time.Hour)
	require.NoError(t, f.repo.CreateForLogin(context.Background(), tok))
	return plain
}

func (f *sessionFixture) activeTokens(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRow(context.Background(),
		`SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, f.userID).Scan(&n))
	return n
}

func TestSessionRepository_CreateForLogin(t *testing.T) {
	f := newSessionFixture(t)

	f.login(t)

	assert.Equal(t, 1, f.activeTokens(t))
	u, err := NewUserRepository(f.db).GetByID(context.Background(), f.userID)
	require.NoError(t, err)
	assert.NotNil(t, u.LastLoginAt, "login time recorded in the same transaction")
}

func TestSessionRepository_Rotate_Success(t *testing.T) {
	f := newSessionFixture(t)
	ctx := context.Background()
	r1 := f.login(t)
	r2, next := f.newToken(t, time.Hour)
	next.UserID = uuid.Nil // Rotate must fill it in from the old token

	userID, err := f.repo.Rotate(ctx, auth.HashRefreshToken(r1), next)

	require.NoError(t, err)
	assert.Equal(t, f.userID, userID)
	assert.Equal(t, 1, f.activeTokens(t), "old revoked, new active")

	// The old token points to its replacement (the audit trail of rotations).
	var replacedBy uuid.UUID
	var newID uuid.UUID
	require.NoError(t, f.db.QueryRow(ctx,
		`SELECT replaced_by_id FROM refresh_tokens WHERE token_hash = $1`, auth.HashRefreshToken(r1)).Scan(&replacedBy))
	require.NoError(t, f.db.QueryRow(ctx,
		`SELECT id FROM refresh_tokens WHERE token_hash = $1`, auth.HashRefreshToken(r2)).Scan(&newID))
	assert.Equal(t, newID, replacedBy)
}

func TestSessionRepository_Rotate_Invalid(t *testing.T) {
	f := newSessionFixture(t)
	ctx := context.Background()

	t.Run("unknown token", func(t *testing.T) {
		_, next := f.newToken(t, time.Hour)
		_, err := f.repo.Rotate(ctx, auth.HashRefreshToken("never-issued"), next)
		assert.ErrorIs(t, err, model.ErrRefreshTokenInvalid)
	})

	t.Run("expired token", func(t *testing.T) {
		plain, expired := f.newToken(t, -time.Minute)
		require.NoError(t, f.repo.CreateForLogin(ctx, expired))
		_, next := f.newToken(t, time.Hour)

		_, err := f.repo.Rotate(ctx, auth.HashRefreshToken(plain), next)

		assert.ErrorIs(t, err, model.ErrRefreshTokenInvalid)
	})
}

func TestSessionRepository_Rotate_ReuseRevokesEverything(t *testing.T) {
	f := newSessionFixture(t)
	ctx := context.Background()
	otherDevice := f.login(t) // a second, unrelated session (e.g. the user's tablet)
	r1 := f.login(t)
	_, next := f.newToken(t, time.Hour)
	_, err := f.repo.Rotate(ctx, auth.HashRefreshToken(r1), next) // r1 → r2
	require.NoError(t, err)
	require.Equal(t, 2, f.activeTokens(t), "tablet + r2")

	// An attacker presents the stolen, already-rotated r1.
	_, attackerNext := f.newToken(t, time.Hour)
	_, err = f.repo.Rotate(ctx, auth.HashRefreshToken(r1), attackerNext)

	assert.ErrorIs(t, err, model.ErrRefreshTokenReused)
	assert.Equal(t, 0, f.activeTokens(t),
		"every session revoked, and the revocation was COMMITTED despite the error")

	_, tabletNext := f.newToken(t, time.Hour)
	_, err = f.repo.Rotate(ctx, auth.HashRefreshToken(otherDevice), tabletNext)
	assert.Error(t, err, "other sessions are revoked too")
}

// Regression test: a token revoked by LOGOUT (no successor) is merely invalid.
// Treating it as theft would log a false alarm and log the user out everywhere.
func TestSessionRepository_Rotate_AfterLogoutIsInvalidNotReuse(t *testing.T) {
	f := newSessionFixture(t)
	ctx := context.Background()
	f.login(t) // another device
	r1 := f.login(t)
	require.NoError(t, f.repo.Revoke(ctx, auth.HashRefreshToken(r1))) // logout
	_, next := f.newToken(t, time.Hour)

	_, err := f.repo.Rotate(ctx, auth.HashRefreshToken(r1), next)

	assert.ErrorIs(t, err, model.ErrRefreshTokenInvalid)
	assert.NotErrorIs(t, err, model.ErrRefreshTokenReused)
	assert.Equal(t, 1, f.activeTokens(t), "the other device must stay logged in")
}

// Two requests rotate the SAME token at the same moment. Exactly one may
// succeed. This is what SELECT ... FOR UPDATE guarantees.
func TestSessionRepository_Rotate_ConcurrentOnlyOneWins(t *testing.T) {
	f := newSessionFixture(t)
	r1 := f.login(t)

	const attempts = 20

	// Warm up the pool: open `attempts` connections in advance. Otherwise each
	// goroutine first spends milliseconds connecting, which staggers them so
	// the rotations never actually overlap and the test proves nothing.
	var warm sync.WaitGroup
	for range attempts {
		warm.Add(1)
		go func() {
			defer warm.Done()
			_, err := f.db.Exec(context.Background(), `SELECT pg_sleep(0.05)`)
			assert.NoError(t, err)
		}()
	}
	warm.Wait()
	var (
		wg     sync.WaitGroup
		start  = make(chan struct{})
		errs   = make([]error, attempts)
		hashR1 = auth.HashRefreshToken(r1)
	)
	for i := range attempts {
		_, next := f.newToken(t, time.Hour)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // all goroutines start together
			_, errs[i] = f.repo.Rotate(context.Background(), hashR1, next)
		}()
	}
	close(start)
	wg.Wait()

	successes, reused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, model.ErrRefreshTokenReused):
			reused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, successes, "exactly one rotation may succeed")
	assert.Equal(t, attempts-1, reused, "the others see a revoked token")
}

func TestSessionRepository_Revoke(t *testing.T) {
	f := newSessionFixture(t)
	ctx := context.Background()
	r1 := f.login(t)

	require.NoError(t, f.repo.Revoke(ctx, auth.HashRefreshToken(r1)))
	assert.Equal(t, 0, f.activeTokens(t))

	assert.NoError(t, f.repo.Revoke(ctx, auth.HashRefreshToken(r1)), "idempotent")
	assert.NoError(t, f.repo.Revoke(ctx, auth.HashRefreshToken("unknown")), "unknown token is not an error")
}

func TestSessionRepository_RevokeAllForUser(t *testing.T) {
	f := newSessionFixture(t)
	f.login(t)
	f.login(t)
	require.Equal(t, 2, f.activeTokens(t))

	require.NoError(t, f.repo.RevokeAllForUser(context.Background(), f.userID))

	assert.Equal(t, 0, f.activeTokens(t))
}
