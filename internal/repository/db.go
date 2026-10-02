// Package repository is the data-access layer: the only place that talks SQL.
//
// Rules for this layer:
//   - no business rules (those belong to the service)
//   - no HTTP knowledge
//   - database errors are translated into model errors (model.ErrNotFound, ...)
package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the set of methods shared by *pgxpool.Pool and pgx.Tx.
// Repositories depend on it, so the same code can run on the pool or inside a
// transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Postgres error codes we react to.
// Full list: https://www.postgresql.org/docs/current/errcodes-appendix.html
const pgUniqueViolation = "23505"
