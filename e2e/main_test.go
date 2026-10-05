// Package e2e runs the real API binary against a real database and talks to
// it only over HTTP, like a client would.
//
// Unit tests check one layer with fakes; repository tests check SQL. These
// tests check what neither can: the wiring in cmd/api (config, middleware
// order, every layer connected), and every endpoint through the whole stack.
//
// Emails (password reset codes) go to a fake SMTP server inside the test
// process (smtp_test.go), so no Mailpit is needed.
//
// Needs TEST_DATABASE_URL (migrated); skipped without it. Run with:
//
//	make test-e2e
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	adminEmail    = "platform.admin@e2e.test"
	adminPassword = "e2e platform admin password"
	// 48 bytes: passes the 32-byte minimum. Only for this throwaway server.
	jwtSecret = "e2e-secret-e2e-secret-e2e-secret-e2e-secret-e2e!"
)

var (
	// baseURL is where the API under test listens, e.g. http://127.0.0.1:53817.
	baseURL string
	// mailbox receives every email the API sends.
	mailbox *inbox
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

// runMain is TestMain's body; returning (instead of os.Exit) lets the
// deferred cleanup (stopping the server, removing binaries) run.
func runMain(m *testing.M) int {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		fmt.Println("e2e: TEST_DATABASE_URL not set; skipping (run `make test-e2e`)")
		return 0
	}

	pool, err := connect(dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: database:", err)
		return 1
	}
	defer pool.Close()

	if err := resetDatabase(pool); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: reset database:", err)
		return 1
	}
	restoreLimits, err := relaxAuthRateLimit(pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: rate limits:", err)
		return 1
	}
	defer restoreLimits()

	mailbox, err = startInbox()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: fake smtp:", err)
		return 1
	}
	defer mailbox.Close()

	binDir, err := os.MkdirTemp("", "eld-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer os.RemoveAll(binDir)

	apiBin := filepath.Join(binDir, "api")
	adminBin := filepath.Join(binDir, "createadmin")
	for bin, pkg := range map[string]string{apiBin: "./cmd/api", adminBin: "./cmd/createadmin"} {
		if err := goBuild(bin, pkg); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: build", pkg+":", err)
			return 1
		}
	}

	port, err := freePort()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: find free port:", err)
		return 1
	}
	env := serverEnv(dbURL, port, mailbox.Port())

	// The first platform admin comes from the CLI, exactly as in production.
	create := exec.Command(adminBin, "-email="+adminEmail, "-first-name=Platform", "-last-name=Admin")
	create.Env = append(env, "ADMIN_PASSWORD="+adminPassword)
	if out, err := create.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: createadmin: %v\n%s", err, out)
		return 1
	}

	var logs bytes.Buffer
	api := exec.Command(apiBin)
	api.Env = env
	api.Stdout, api.Stderr = &logs, &logs
	if err := api.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: start api:", err)
		return 1
	}
	baseURL = "http://127.0.0.1:" + port

	if err := waitReady(baseURL+"/ready", 15*time.Second); err != nil {
		_ = api.Process.Kill()
		_ = api.Wait()
		fmt.Fprintf(os.Stderr, "e2e: api not ready: %v\n--- api logs ---\n%s", err, logs.String())
		return 1
	}

	code := m.Run()

	// SIGTERM, like Docker/Kubernetes: the API must shut down gracefully
	// and exit 0. A non-zero exit fails the run too.
	_ = api.Process.Signal(syscall.SIGTERM)
	if err := api.Wait(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: api did not shut down cleanly:", err)
		code = 1
	}
	if code != 0 {
		fmt.Fprintf(os.Stderr, "--- api logs ---\n%s", logs.String())
	}
	return code
}

// serverEnv is the COMPLETE environment of the API and CLI. Built from
// scratch rather than inherited, so values exported from .env by make
// (DATABASE_URL of the dev database, REDIS_URL, the real SMTP server) never
// leak in. Without REDIS_URL rate limits count in memory, fresh every run.
func serverEnv(dbURL, port, smtpPort string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"APP_ENV=development", // Swagger UI on, like a developer's machine
		"LOG_LEVEL=warn",
		"GIN_MODE=release", // no route list in the logs printed on failure
		"HTTP_PORT=" + port,
		"DATABASE_URL=" + dbURL,
		"JWT_SECRET=" + jwtSecret,
		"SMTP_HOST=127.0.0.1",
		"SMTP_PORT=" + smtpPort,
		"SMTP_FROM=noreply@e2e.test",
	}
}

func connect(dbURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, err
	}
	// Safety guard: this deletes data. Never run it on a real database.
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		return nil, fmt.Errorf("refusing to run: database %q does not end with _test", cfg.ConnConfig.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// resetDatabase empties the test tables, keeping seed data from migrations
// (permissions, system roles, rate limits). Same order and reasons as
// newTestDB in internal/repository.
func resetDatabase(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `
		TRUNCATE audit_logs;
		DELETE FROM eld_devices;
		DELETE FROM trucks;
		DELETE FROM drivers;
		DELETE FROM refresh_tokens;
		DELETE FROM password_resets;
		DELETE FROM user_roles;
		DELETE FROM users;
		DELETE FROM roles WHERE company_id IS NOT NULL;
		DELETE FROM companies;`)
	return err
}

// relaxAuthRateLimit raises the per-IP limit on /auth/* before the API
// starts: every request of the suite comes from 127.0.0.1, so the real 20
// per minute would turn later tests into 429s. The other rules keep their
// real values; TestRateLimits checks that limiting itself works. The
// returned function restores every rule, because other packages' tests
// share this database.
func relaxAuthRateLimit(pool *pgxpool.Pool) (restore func(), err error) {
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT name, max_requests, window_seconds FROM rate_limits`)
	if err != nil {
		return nil, err
	}
	type rule struct {
		name          string
		limit, window int
	}
	var saved []rule
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.name, &r.limit, &r.window); err != nil {
			rows.Close()
			return nil, err
		}
		saved = append(saved, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := pool.Exec(ctx,
		`UPDATE rate_limits SET max_requests = 10000 WHERE name = 'auth_requests'`); err != nil {
		return nil, err
	}
	return func() {
		for _, r := range saved {
			if _, err := pool.Exec(ctx,
				`UPDATE rate_limits SET max_requests = $2, window_seconds = $3, updated_by = NULL WHERE name = $1`,
				r.name, r.limit, r.window); err != nil {
				fmt.Fprintln(os.Stderr, "e2e: restore rate limit", r.name+":", err)
			}
		}
	}, nil
}

// goBuild compiles pkg (relative to the repository root) into out.
func goBuild(out, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = ".." // tests run in e2e/; packages are relative to the root
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, b)
	}
	return nil
}

// freePort asks the OS for an unused port. There is a tiny window where
// another process could take it before the API binds; fine for tests.
func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

// waitReady polls url until it answers 200 or the timeout passes.
func waitReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
