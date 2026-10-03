// Package database opens the PostgreSQL connection pool and the Redis client.
package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a pgx connection pool and verifies the database is reachable.
//
// A pool keeps several open connections and lends one to each query, so
// requests do not pay the cost of opening a new connection every time.
// *pgxpool.Pool is safe for concurrent use: create it once in main and share it.
//
// Pool size and timeouts can be tuned with URL parameters, for example:
//
//	postgres://...?pool_max_conns=10&pool_max_conn_idle_time=5m
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Do not include the URL in the error: it contains the password.
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// NewWithConfig connects lazily, so ping now to fail fast at startup
	// instead of on the first real request.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}
