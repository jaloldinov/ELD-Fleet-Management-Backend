package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Guard test (no database needed): repository methods must get their
// connection via r.q(ctx). A direct r.db call would silently run OUTSIDE the
// caller's transaction, and no other test would necessarily notice.
func TestRepositoriesOnlyUseConnFromContext(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(src), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue // comments may talk about r.db
			}
			if strings.Contains(code, "r.db") && !strings.Contains(code, "return conn(ctx, r.db)") {
				t.Errorf("%s:%d uses r.db directly; use r.q(ctx) so transactions work:\n\t%s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

var errBoom = errors.New("boom")

func TestTxManager_RollbackAcrossRepositories(t *testing.T) {
	db := newTestDB(t)
	tx := NewTxManager(db)
	companies := NewCompanyRepository(db)
	users := NewUserRepository(db)
	ctx := context.Background()

	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		c, err := companies.Create(ctx, model.Company{Name: "Acme", DOTNumber: "1", Timezone: "UTC", HOSCycle: model.HOSCycle70in8})
		require.NoError(t, err)
		_, err = users.Create(ctx, newUser(c.ID, "jane@acme.com"), []string{model.RoleDriver})
		require.NoError(t, err)
		return errBoom // a later step fails
	})

	assert.ErrorIs(t, err, errBoom)
	_, total, err := companies.List(ctx, model.CompanyFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Zero(t, total, "company insert rolled back")
	assert.Zero(t, countUsers(t, db), "user insert (from ANOTHER repository) rolled back too")
}

func TestTxManager_Commit(t *testing.T) {
	db := newTestDB(t)
	tx := NewTxManager(db)
	companies := NewCompanyRepository(db)
	ctx := context.Background()

	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		_, err := companies.Create(ctx, model.Company{Name: "Acme", DOTNumber: "1", Timezone: "UTC", HOSCycle: model.HOSCycle70in8})
		return err
	})

	require.NoError(t, err)
	_, total, _ := companies.List(ctx, model.CompanyFilter{}, 10, 0)
	assert.Equal(t, 1, total)
}

// Inside a transaction, UserRepository.Create's own BeginFunc becomes a
// savepoint. Its failure rolls back only the savepoint; the outer function
// decides what happens to the whole transaction.
func TestTxManager_NestedRepositoryTransactionJoins(t *testing.T) {
	db := newTestDB(t)
	tx := NewTxManager(db)
	companies := NewCompanyRepository(db)
	users := NewUserRepository(db)
	ctx := context.Background()

	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		c, err := companies.Create(ctx, model.Company{Name: "Acme", DOTNumber: "1", Timezone: "UTC", HOSCycle: model.HOSCycle70in8})
		require.NoError(t, err)
		_, err = users.Create(ctx, newUser(c.ID, "jane@acme.com"), []string{"no_such_role"})
		require.ErrorIs(t, err, model.ErrRoleNotFound)
		return err // propagate: the whole unit of work fails
	})

	assert.ErrorIs(t, err, model.ErrRoleNotFound)
	_, total, _ := companies.List(ctx, model.CompanyFilter{}, 10, 0)
	assert.Zero(t, total)
}

func TestTxManager_WithinTxJoinsExistingTransaction(t *testing.T) {
	db := newTestDB(t)
	tx := NewTxManager(db)
	companies := NewCompanyRepository(db)
	ctx := context.Background()

	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		// e.g. one service calling another service that also uses WithinTx.
		inner := tx.WithinTx(ctx, func(ctx context.Context) error {
			_, err := companies.Create(ctx, model.Company{Name: "Acme", DOTNumber: "1", Timezone: "UTC", HOSCycle: model.HOSCycle70in8})
			return err
		})
		require.NoError(t, inner)
		return errBoom // the OUTER failure must undo the inner work too
	})

	assert.ErrorIs(t, err, errBoom)
	_, total, _ := companies.List(ctx, model.CompanyFilter{}, 10, 0)
	assert.Zero(t, total, "inner WithinTx joined the outer transaction")
}

// One picture: a change committed by someone else WHILE we read is not seen
// by our later reads. With WithinTx (READ COMMITTED) it would be: the
// second read below would count 2.
func TestTxManager_WithinSnapshot_OnePicture(t *testing.T) {
	db := newTestDB(t)
	tx := NewTxManager(db)
	companies := NewCompanyRepository(db)
	ctx := context.Background()
	createCompany(t, companies, "Alpha", "1")

	count := func(ctx context.Context) int {
		_, total, err := companies.List(ctx, model.CompanyFilter{}, 10, 0)
		require.NoError(t, err)
		return total
	}

	for _, tc := range []struct {
		name   string
		within func(context.Context, func(context.Context) error) error
		second int
	}{
		{"snapshot: still the first picture", tx.WithinSnapshot, 1},
		{"read committed: each statement sees the latest", tx.WithinTx, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(ctx, `DELETE FROM companies WHERE dot_number = '2'`)
			require.NoError(t, err)
			err = tc.within(ctx, func(ctx context.Context) error {
				assert.Equal(t, 1, count(ctx))
				// Someone else commits a new company meanwhile (the pool, not
				// our transaction).
				createCompany(t, companies, "Bravo", "2")
				assert.Equal(t, tc.second, count(ctx))
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestTxManager_WithinSnapshot_IsReadOnly(t *testing.T) {
	db := newTestDB(t)
	tx := NewTxManager(db)
	companies := NewCompanyRepository(db)

	err := tx.WithinSnapshot(context.Background(), func(ctx context.Context) error {
		_, err := companies.Create(ctx, model.Company{Name: "Acme", DOTNumber: "1", Timezone: "UTC", HOSCycle: model.HOSCycle70in8})
		return err
	})

	assert.ErrorContains(t, err, "read-only transaction")
}
