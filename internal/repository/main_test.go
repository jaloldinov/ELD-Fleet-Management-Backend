package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
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

	// Enough connections for concurrency tests to really run in parallel
	// (the default is max(4, number of CPUs)).
	cfg.MaxConns = 25

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
		TRUNCATE audit_logs; -- append-only: DELETE is refused, TRUNCATE is allowed
		TRUNCATE stripe_events;
		DELETE FROM invoices;
		DELETE FROM subscriptions; -- items go with them (ON DELETE CASCADE)
		DELETE FROM prices;
		DELETE FROM products;
		DELETE FROM duty_log_certifications;
		DELETE FROM duty_status_events;       -- logbook: before drivers and trucks
		DELETE FROM driver_truck_assignments; -- history: before drivers and trucks
		DELETE FROM eld_truck_assignments;    -- history: before devices and trucks
		DELETE FROM eld_events;               -- telemetry: before devices and trucks
		DELETE FROM eld_device_states;
		DELETE FROM eld_devices;
		DELETE FROM trucks;
		DELETE FROM drivers;
		DELETE FROM refresh_tokens;
		DELETE FROM password_resets;
		DELETE FROM user_roles;
		DELETE FROM users;
		DELETE FROM roles WHERE company_id IS NOT NULL;
		DELETE FROM companies;`)
	require.NoError(t, err)

	// Like the API at startup: roles and permissions defined in the code
	// exist even if no migration created them.
	_, err = NewRBACRepository(pool).Sync(context.Background(), model.SystemRoles(), model.Permissions())
	require.NoError(t, err)

	return pool
}
