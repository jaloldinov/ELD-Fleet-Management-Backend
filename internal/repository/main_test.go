package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// newTestDB connects to TEST_DATABASE_URL and empties all tables, so each test
// starts from a clean state. Tests are skipped when the variable is not set,
// so `go test ./...` still works without Docker.
//
// Run with: make test-integration
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test-integration`")
	}

	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	// Safety guard: these tests delete data. Never run them on a real database.
	require.True(t, strings.HasSuffix(cfg.ConnConfig.Database, "_test"),
		"refusing to run: database %q does not end with _test", cfg.ConnConfig.Database)

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Remove test data, children before parents (foreign keys), but keep the
	// seed data from migrations (permissions, system roles, role_permissions).
	//
	// Do NOT use "TRUNCATE companies CASCADE": CASCADE empties every table
	// that references companies *entirely*, including roles, which would wipe
	// the seeded system roles.
	//
	// Exec without arguments uses Postgres' simple protocol, which allows
	// several statements in one call.
	_, err = pool.Exec(context.Background(), `
		DELETE FROM user_roles;
		DELETE FROM users;
		DELETE FROM roles WHERE company_id IS NOT NULL;
		DELETE FROM companies;`)
	require.NoError(t, err)

	return pool
}
