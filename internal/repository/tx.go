package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Transactions that span several repositories ("unit of work").
//
// The service decides where a transaction starts and ends, because only it
// knows which writes form one business operation (e.g. user + driver). The
// open transaction travels in the context; every repository method gets its
// connection through conn(ctx, db), so it automatically joins the
// transaction when there is one.

type txKey struct{}

// TxManager runs functions inside a database transaction.
type TxManager struct {
	db DBTX
}

func NewTxManager(db DBTX) *TxManager {
	return &TxManager{db: db}
}

// WithinTx runs fn in a transaction: COMMIT if fn returns nil, ROLLBACK if it
// returns an error or panics. Repositories called with the ctx passed to fn
// use the transaction. If ctx already carries a transaction, fn simply joins
// it (no nested BEGIN), so services can call each other freely.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	return pgx.BeginFunc(ctx, m.db, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// errNoTx: a row lock was requested outside a transaction. Without one, each
// statement is its own transaction and the lock is released right after the
// SELECT, before the caller acts on what it read: the lock would protect
// nothing. This is a programming error, so it is not a model error.
var errNoTx = errors.New("row lock requested outside a transaction")

// requireTx returns errNoTx unless ctx carries a transaction.
func requireTx(ctx context.Context) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return errNoTx
	}
	return nil
}

// conn returns the transaction carried by ctx, or db when there is none.
// EVERY repository method must use it (through r.q(ctx)): a method that used
// r.db directly would silently run outside the caller's transaction.
func conn(ctx context.Context, db DBTX) DBTX {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return db
}
