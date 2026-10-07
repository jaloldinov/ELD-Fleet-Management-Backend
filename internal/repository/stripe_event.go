package repository

import (
	"context"
	"fmt"
)

// StripeEventRepository remembers which Stripe webhooks were handled, so
// each event is processed once even when Stripe delivers it again.
type StripeEventRepository struct {
	db DBTX
}

func NewStripeEventRepository(db DBTX) *StripeEventRepository {
	return &StripeEventRepository{db: db}
}

func (r *StripeEventRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Start records an attempt to handle the event and reports whether it was
// already processed (then there is nothing to do).
func (r *StripeEventRepository) Start(ctx context.Context, id, eventType string) (alreadyProcessed bool, err error) {
	err = r.q(ctx).QueryRow(ctx, `
		INSERT INTO stripe_events (id, type, attempts) VALUES ($1, $2, 1)
		ON CONFLICT (id) DO UPDATE SET attempts = stripe_events.attempts + 1
		RETURNING processed_at IS NOT NULL`,
		id, eventType,
	).Scan(&alreadyProcessed)
	if err != nil {
		return false, fmt.Errorf("start stripe event: %w", err)
	}
	return alreadyProcessed, nil
}

// MarkProcessed: done; a repeated delivery will be skipped.
func (r *StripeEventRepository) MarkProcessed(ctx context.Context, id string) error {
	if _, err := r.q(ctx).Exec(ctx, `
		UPDATE stripe_events SET processed_at = now(), last_error = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark stripe event processed: %w", err)
	}
	return nil
}

// MarkFailed keeps the error for investigation; the event stays
// unprocessed, so Stripe's retry runs it again.
func (r *StripeEventRepository) MarkFailed(ctx context.Context, id, msg string) error {
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	if _, err := r.q(ctx).Exec(ctx, `UPDATE stripe_events SET last_error = $2 WHERE id = $1`, id, msg); err != nil {
		return fmt.Errorf("mark stripe event failed: %w", err)
	}
	return nil
}
