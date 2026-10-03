package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const rateLimitColumns = `name, max_requests, window_seconds, description, updated_at, updated_by`

type RateLimitRepository struct {
	db DBTX
}

func NewRateLimitRepository(db DBTX) *RateLimitRepository {
	return &RateLimitRepository{db: db}
}

func (r *RateLimitRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// List returns every rule, ordered by name. A handful of rows: no paging.
func (r *RateLimitRepository) List(ctx context.Context) ([]model.RateLimit, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+rateLimitColumns+` FROM rate_limits ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list rate limits: %w", err)
	}
	limits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.RateLimit, error) {
		rl, err := scanRateLimit(row)
		if err != nil {
			return model.RateLimit{}, err
		}
		return *rl, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan rate limits: %w", err)
	}
	return limits, nil
}

func (r *RateLimitRepository) GetByName(ctx context.Context, name string) (*model.RateLimit, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+rateLimitColumns+` FROM rate_limits WHERE name = $1`, name)
	rl, err := scanRateLimit(row)
	if err != nil {
		return nil, mapRateLimitError("get rate limit", err)
	}
	return rl, nil
}

// Update saves the limit and window (the name and description are fixed by
// migrations).
func (r *RateLimitRepository) Update(ctx context.Context, rl model.RateLimit) (*model.RateLimit, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE rate_limits
		SET max_requests = $2, window_seconds = $3, updated_by = $4
		WHERE name = $1
		RETURNING `+rateLimitColumns,
		rl.Name, rl.Limit, int(rl.Window/time.Second), rl.UpdatedBy,
	)
	updated, err := scanRateLimit(row)
	if err != nil {
		return nil, mapRateLimitError("update rate limit", err)
	}
	return updated, nil
}

func scanRateLimit(row pgx.Row) (*model.RateLimit, error) {
	var rl model.RateLimit
	var windowSeconds int
	if err := row.Scan(&rl.Name, &rl.Limit, &windowSeconds, &rl.Description, &rl.UpdatedAt, &rl.UpdatedBy); err != nil {
		return nil, err
	}
	rl.Window = time.Duration(windowSeconds) * time.Second
	return &rl, nil
}

func mapRateLimitError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
