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

// PasswordResetRepository stores "forgot password" codes and reset tokens.
//
// Every check-and-change is ONE statement (UPDATE ... WHERE <still valid>),
// so two parallel requests cannot both use the same attempt or token.
// Expiry is computed with the database clock (now()), the same clock that
// checks it.
type PasswordResetRepository struct {
	db DBTX
}

func NewPasswordResetRepository(db DBTX) *PasswordResetRepository {
	return &PasswordResetRepository{db: db}
}

func (r *PasswordResetRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Replace starts a new reset for the user; any pending one stops working.
func (r *PasswordResetRepository) Replace(ctx context.Context, userID uuid.UUID, codeHash []byte, ttl time.Duration) error {
	return pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM password_resets WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
			return fmt.Errorf("delete pending resets: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO password_resets (user_id, code_hash, code_expires_at)
			VALUES ($1, $2, now() + make_interval(secs => $3))`,
			userID, codeHash, ttl.Seconds()); err != nil {
			return fmt.Errorf("insert reset: %w", err)
		}
		return nil
	})
}

// GetPending returns the user's reset whose code can still be verified, or
// model.ErrNotFound.
func (r *PasswordResetRepository) GetPending(ctx context.Context, userID uuid.UUID) (*model.PasswordReset, error) {
	var pr model.PasswordReset
	err := r.q(ctx).QueryRow(ctx, `
		SELECT id, user_id, code_hash, attempts, code_expires_at, created_at
		FROM password_resets
		WHERE user_id = $1 AND verified_at IS NULL AND used_at IS NULL AND code_expires_at > now()`,
		userID).Scan(&pr.ID, &pr.UserID, &pr.CodeHash, &pr.Attempts, &pr.CodeExpiresAt, &pr.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pending reset: %w", err)
	}
	return &pr, nil
}

// ConsumeAttempt uses up one of the code's attempts BEFORE it is compared,
// so every guess counts, also parallel ones. model.ErrNotFound when none is
// left (or the code expired meanwhile).
func (r *PasswordResetRepository) ConsumeAttempt(ctx context.Context, id uuid.UUID, maxAttempts int) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE password_resets SET attempts = attempts + 1
		WHERE id = $1 AND attempts < $2
		  AND verified_at IS NULL AND used_at IS NULL AND code_expires_at > now()`,
		id, maxAttempts)
	if err != nil {
		return fmt.Errorf("consume attempt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// MarkVerified records the correct code and stores the reset token's hash.
// model.ErrNotFound if it was already verified (the code is single use).
func (r *PasswordResetRepository) MarkVerified(ctx context.Context, id uuid.UUID, tokenHash []byte, ttl time.Duration) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE password_resets
		SET verified_at = now(), token_hash = $2, token_expires_at = now() + make_interval(secs => $3)
		WHERE id = $1 AND verified_at IS NULL AND used_at IS NULL`,
		id, tokenHash, ttl.Seconds())
	if err != nil {
		return fmt.Errorf("mark reset verified: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// UseToken marks the reset token as used and returns its user. A token
// works once; model.ErrNotFound if unknown, used or expired.
func (r *PasswordResetRepository) UseToken(ctx context.Context, tokenHash []byte) (uuid.UUID, error) {
	var userID uuid.UUID
	err := r.q(ctx).QueryRow(ctx, `
		UPDATE password_resets SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND token_expires_at > now()
		RETURNING user_id`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, model.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("use reset token: %w", err)
	}
	return userID, nil
}

// DeleteCreatedBefore removes old resets (the cleanup job).
func (r *PasswordResetRepository) DeleteCreatedBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.q(ctx).Exec(ctx, `DELETE FROM password_resets WHERE created_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete old resets: %w", err)
	}
	return tag.RowsAffected(), nil
}
