package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// SessionRepository stores refresh tokens (one active token per login session).
type SessionRepository struct {
	db DBTX
}

func NewSessionRepository(db DBTX) *SessionRepository {
	return &SessionRepository{db: db}
}

// q returns the connection to use: the caller's transaction if ctx carries
// one, otherwise the pool.
func (r *SessionRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// CreateForLogin stores the first refresh token of a new session and records
// the login time, in one transaction.
func (r *SessionRepository) CreateForLogin(ctx context.Context, t model.RefreshToken) error {
	err := pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
		if err := insertRefreshToken(ctx, tx, t); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, t.UserID)
		return err
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// Rotate exchanges the token with hash presentedHash for next, atomically.
// It returns the user the session belongs to. next.UserID is filled in here.
//
//   - unknown, expired, or revoked by logout/revoke-all → model.ErrRefreshTokenInvalid
//   - already ROTATED token (it has a successor) = reuse = theft → ALL the
//     user's active tokens are revoked (committed!), then model.ErrRefreshTokenReused
//   - valid token → old revoked + linked to next, next inserted
func (r *SessionRepository) Rotate(ctx context.Context, presentedHash []byte, next model.RefreshToken) (uuid.UUID, error) {
	var (
		userID  uuid.UUID
		outcome error // decided inside the transaction, returned after COMMIT
	)

	err := pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
		var (
			oldID                     uuid.UUID
			rotated, revoked, expired bool
		)
		// FOR UPDATE locks this row until COMMIT/ROLLBACK. A second request
		// rotating the same token at the same moment WAITS here, and then sees
		// rotated = true. Without the lock both could read "valid" and both
		// get a new token.
		err := tx.QueryRow(ctx, `
			SELECT id, user_id,
			       replaced_by_id IS NOT NULL,
			       revoked_at IS NOT NULL,
			       expires_at <= now()
			FROM refresh_tokens
			WHERE token_hash = $1
			FOR UPDATE`,
			presentedHash,
		).Scan(&oldID, &userID, &rotated, &revoked, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = model.ErrRefreshTokenInvalid
			return nil
		}
		if err != nil {
			return err
		}

		switch {
		case rotated:
			// This token was already exchanged for a newer one, so a copy
			// exists somewhere = theft. Revoke everything, and COMMIT that
			// (return nil): returning an error would roll the revocation back.
			if _, err := tx.Exec(ctx, revokeAllForUserSQL, userID); err != nil {
				return err
			}
			outcome = model.ErrRefreshTokenReused
			return nil
		case revoked, expired:
			// Ended normally (logout, revoke-all) or too old: just invalid.
			// NOT theft: e.g. an app retrying with its token after logout.
			outcome = model.ErrRefreshTokenInvalid
			return nil
		}

		next.UserID = userID
		if err := insertRefreshToken(ctx, tx, next); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE refresh_tokens
			SET revoked_at = now(),
			    replaced_by_id = (SELECT id FROM refresh_tokens WHERE token_hash = $2)
			WHERE id = $1`,
			oldID, next.TokenHash,
		)
		return err
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("rotate refresh token: %w", err)
	}
	if outcome != nil {
		return uuid.Nil, outcome
	}
	return userID, nil
}

// Revoke ends one session (logout). Unknown or already revoked tokens are
// ignored, so logout is idempotent and reveals nothing.
func (r *SessionRepository) Revoke(ctx context.Context, tokenHash []byte) error {
	_, err := r.q(ctx).Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL`,
		tokenHash,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

// RevokeAllForUser ends every session of a user (theft, deactivation, ...).
func (r *SessionRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.q(ctx).Exec(ctx, revokeAllForUserSQL, userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

const revokeAllForUserSQL = `
	UPDATE refresh_tokens SET revoked_at = now()
	WHERE user_id = $1 AND revoked_at IS NULL`

func insertRefreshToken(ctx context.Context, db DBTX, t model.RefreshToken) error {
	_, err := db.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at, user_agent, ip)
		VALUES ($1, $2, $3, $4, $5)`,
		t.UserID, t.TokenHash, t.ExpiresAt, t.UserAgent, t.IP,
	)
	return err
}

// DeleteExpired permanently deletes tokens that expired before `before`, in
// batches of batchSize, and returns how many it deleted.
//
// Batches keep each statement short: one huge DELETE would hold locks for a
// long time and block logins. Oldest first matters: a rotated token always
// expires before its successor, so a token is deleted no later than the
// token it points to with replaced_by_id, and the foreign key holds at every
// batch boundary.
func (r *SessionRepository) DeleteExpired(ctx context.Context, before time.Time, batchSize int) (int64, error) {
	var total int64
	for {
		tag, err := r.q(ctx).Exec(ctx, `
			DELETE FROM refresh_tokens
			WHERE id IN (
				SELECT id FROM refresh_tokens
				WHERE expires_at < $1
				ORDER BY expires_at, id
				LIMIT $2
			)`, before, batchSize)
		if err != nil {
			return total, fmt.Errorf("delete expired refresh tokens: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batchSize) {
			return total, nil // last (partial) batch
		}
		if err := ctx.Err(); err != nil {
			return total, err // shutting down: stop between batches
		}
	}
}
