package database

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPool_InvalidURL(t *testing.T) {
	pool, err := NewPool(context.Background(), "not a url ://")

	assert.ErrorContains(t, err, "parse database url")
	assert.Nil(t, pool)
}

func TestNewPool_UnreachableDatabase(t *testing.T) {
	// Port 1 is never a Postgres server; the short timeout keeps the test fast.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable")

	assert.ErrorContains(t, err, "ping database")
	assert.Nil(t, pool)
}

// This test needs a real database. It runs only when TEST_DATABASE_URL is set
// (e.g. by `make test-integration`), so `go test ./...` works without Docker.
func TestNewPool_RealDatabase(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	pool, err := NewPool(context.Background(), url)
	require.NoError(t, err)
	defer pool.Close()

	var one int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one)
}
