// Package repository is the data-access layer: the only place that talks SQL.
//
// Rules for this layer:
//   - no business rules (those belong to the service)
//   - no HTTP knowledge
//   - database errors are translated into model errors (model.ErrNotFound, ...)
package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the set of methods shared by *pgxpool.Pool and pgx.Tx.
// Repositories depend on it, so the same code can run on the pool or inside a
// transaction.
//
// Begin on the pool starts a transaction; Begin on a pgx.Tx starts a
// savepoint (a "nested transaction"), so repository code that needs a
// transaction also works when the caller already started one.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Postgres error codes we react to.
// Full list: https://www.postgresql.org/docs/current/errcodes-appendix.html
const pgUniqueViolation = "23505"

// isUniqueViolation reports whether err is a unique-constraint violation on
// the given constraint (or unique index) name.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == pgUniqueViolation &&
		pgErr.ConstraintName == constraint
}
