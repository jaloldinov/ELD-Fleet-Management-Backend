package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const subscriptionColumns = `id, company_id, stripe_subscription_id, status, billing_interval,
	current_period_start, current_period_end, cancel_at_period_end, canceled_at, created_at, updated_at,
	stripe_schedule_id, scheduled_change_at`

// SubscriptionRepository stores our copy of companies' Stripe subscriptions.
type SubscriptionRepository struct {
	db DBTX
}

func NewSubscriptionRepository(db DBTX) *SubscriptionRepository {
	return &SubscriptionRepository{db: db}
}

func (r *SubscriptionRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Upsert saves s as Stripe currently sees it: the subscription row (found
// by its Stripe id, created with s.ID if new) and its items, replaced as a
// whole. Items reference our prices by Item.Price.ID.
//
// Runs in a transaction (joining the caller's): readers never see a
// subscription with half of its items.
func (r *SubscriptionRepository) Upsert(ctx context.Context, s model.Subscription) (*model.Subscription, error) {
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
		// company_id is never updated: a subscription cannot move.
		err := tx.QueryRow(ctx, `
			INSERT INTO subscriptions (id, company_id, stripe_subscription_id, status, billing_interval,
			                           current_period_start, current_period_end, cancel_at_period_end, canceled_at,
			                           stripe_schedule_id, scheduled_change_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (stripe_subscription_id) DO UPDATE SET
				status               = EXCLUDED.status,
				billing_interval     = EXCLUDED.billing_interval,
				current_period_start = EXCLUDED.current_period_start,
				current_period_end   = EXCLUDED.current_period_end,
				cancel_at_period_end = EXCLUDED.cancel_at_period_end,
				canceled_at          = EXCLUDED.canceled_at,
				stripe_schedule_id   = EXCLUDED.stripe_schedule_id,
				scheduled_change_at  = EXCLUDED.scheduled_change_at
			RETURNING id`,
			s.ID, s.CompanyID, s.StripeSubscriptionID, s.Status, s.Interval,
			s.CurrentPeriodStart, s.CurrentPeriodEnd, s.CancelAtPeriodEnd, s.CanceledAt,
			s.StripeScheduleID, s.ScheduledChangeAt,
		).Scan(&id)
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `DELETE FROM subscription_items WHERE subscription_id = $1`, id); err != nil {
			return err
		}
		for _, it := range s.Items {
			if _, err := tx.Exec(ctx, `
				INSERT INTO subscription_items (subscription_id, stripe_subscription_item_id, price_id, quantity)
				VALUES ($1, $2, $3, $4)`,
				id, it.StripeSubscriptionItemID, it.Price.ID, it.Quantity,
			); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `DELETE FROM subscription_scheduled_items WHERE subscription_id = $1`, id); err != nil {
			return err
		}
		for _, it := range s.ScheduledItems {
			if _, err := tx.Exec(ctx, `
				INSERT INTO subscription_scheduled_items (subscription_id, price_id, quantity) VALUES ($1, $2, $3)`,
				id, it.Price.ID, it.Quantity,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mapSubscriptionError("upsert subscription", err)
	}
	return r.GetByID(ctx, id)
}

func (r *SubscriptionRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Subscription, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE id = $1`, id)
	return r.one(ctx, row, "get subscription")
}

// GetByStripeID finds our copy of a Stripe subscription (sub_...).
func (r *SubscriptionRepository) GetByStripeID(ctx context.Context, stripeID string) (*model.Subscription, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE stripe_subscription_id = $1`, stripeID)
	return r.one(ctx, row, "get subscription by stripe id")
}

// GetCurrent returns the company's subscription that has not ended
// (model.ErrNotFound if none). The unique index guarantees at most one.
func (r *SubscriptionRepository) GetCurrent(ctx context.Context, companyID uuid.UUID) (*model.Subscription, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+subscriptionColumns+` FROM subscriptions
		WHERE company_id = $1 AND status NOT IN ('canceled', 'incomplete_expired')`, companyID)
	return r.one(ctx, row, "get current subscription")
}

// List returns one page of subscriptions (newest first), with their items.
func (r *SubscriptionRepository) List(ctx context.Context, f model.SubscriptionFilter, limit, offset int) ([]model.Subscription, int, error) {
	const where = `
		WHERE ($1::uuid IS NULL OR company_id = $1)
		  AND ($2 = '' OR status = $2)`

	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM subscriptions`+where,
		f.CompanyID, f.Status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count subscriptions: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, `SELECT `+subscriptionColumns+` FROM subscriptions`+where+`
		ORDER BY id DESC
		LIMIT $3 OFFSET $4`,
		f.CompanyID, f.Status, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list subscriptions: %w", err)
	}
	subs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Subscription, error) {
		s, err := scanSubscription(row)
		if err != nil {
			return model.Subscription{}, err
		}
		return *s, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan subscriptions: %w", err)
	}

	ids := make([]uuid.UUID, len(subs))
	for i := range subs {
		ids[i] = subs[i].ID
	}
	items, err := r.itemsOf(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	scheduled, err := r.scheduledItemsOf(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range subs {
		subs[i].Items = items[subs[i].ID]
		subs[i].ScheduledItems = scheduled[subs[i].ID]
	}
	return subs, total, nil
}

func (r *SubscriptionRepository) one(ctx context.Context, row pgx.Row, op string) (*model.Subscription, error) {
	s, err := scanSubscription(row)
	if err != nil {
		return nil, mapSubscriptionError(op, err)
	}
	items, err := r.itemsOf(ctx, []uuid.UUID{s.ID})
	if err != nil {
		return nil, err
	}
	scheduled, err := r.scheduledItemsOf(ctx, []uuid.UUID{s.ID})
	if err != nil {
		return nil, err
	}
	s.Items, s.ScheduledItems = items[s.ID], scheduled[s.ID]
	return s, nil
}

// scheduledItemsOf loads the scheduled plan of several subscriptions in one
// query (same shape as itemsOf, without Stripe item ids: they do not exist
// until the plan starts).
func (r *SubscriptionRepository) scheduledItemsOf(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID][]model.SubscriptionItem, error) {
	out := make(map[uuid.UUID][]model.SubscriptionItem, len(subIDs))
	for _, id := range subIDs {
		out[id] = []model.SubscriptionItem{}
	}
	if len(subIDs) == 0 {
		return out, nil
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT ss.subscription_id, ss.id, ss.quantity, pr.name,
		       p.id, p.product_id, p.stripe_price_id, p.nickname, p.currency, p.unit_amount,
		       p.billing_interval, p.billing_unit, p.is_active, p.created_at, p.updated_at
		FROM subscription_scheduled_items ss
		JOIN prices p    ON p.id = ss.price_id
		JOIN products pr ON pr.id = p.product_id
		WHERE ss.subscription_id = ANY($1)
		ORDER BY ss.subscription_id, pr.name, ss.id`, subIDs)
	if err != nil {
		return nil, fmt.Errorf("list scheduled items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var subID uuid.UUID
		var it model.SubscriptionItem
		p := &it.Price
		if err := rows.Scan(&subID, &it.ID, &it.Quantity, &it.ProductName,
			&p.ID, &p.ProductID, &p.StripePriceID, &p.Nickname, &p.Currency, &p.UnitAmount,
			&p.Interval, &p.BillingUnit, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan scheduled item: %w", err)
		}
		out[subID] = append(out[subID], it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scheduled items: %w", err)
	}
	return out, nil
}

// itemsOf loads the items of several subscriptions in one query, each with
// its price and product name (what an invoice line shows).
func (r *SubscriptionRepository) itemsOf(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID][]model.SubscriptionItem, error) {
	out := make(map[uuid.UUID][]model.SubscriptionItem, len(subIDs))
	for _, id := range subIDs {
		out[id] = []model.SubscriptionItem{}
	}
	if len(subIDs) == 0 {
		return out, nil
	}

	rows, err := r.q(ctx).Query(ctx, `
		SELECT si.subscription_id, si.id, si.stripe_subscription_item_id, si.quantity, pr.name,
		       p.id, p.product_id, p.stripe_price_id, p.nickname, p.currency, p.unit_amount,
		       p.billing_interval, p.billing_unit, p.is_active, p.created_at, p.updated_at
		FROM subscription_items si
		JOIN prices p    ON p.id = si.price_id
		JOIN products pr ON pr.id = p.product_id
		WHERE si.subscription_id = ANY($1)
		ORDER BY si.subscription_id, pr.name, si.id`, subIDs)
	if err != nil {
		return nil, fmt.Errorf("list subscription items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var subID uuid.UUID
		var it model.SubscriptionItem
		p := &it.Price
		if err := rows.Scan(&subID, &it.ID, &it.StripeSubscriptionItemID, &it.Quantity, &it.ProductName,
			&p.ID, &p.ProductID, &p.StripePriceID, &p.Nickname, &p.Currency, &p.UnitAmount,
			&p.Interval, &p.BillingUnit, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription item: %w", err)
		}
		out[subID] = append(out[subID], it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list subscription items: %w", err)
	}
	return out, nil
}

func scanSubscription(row pgx.Row) (*model.Subscription, error) {
	var s model.Subscription
	err := row.Scan(&s.ID, &s.CompanyID, &s.StripeSubscriptionID, &s.Status, &s.Interval,
		&s.CurrentPeriodStart, &s.CurrentPeriodEnd, &s.CancelAtPeriodEnd, &s.CanceledAt, &s.CreatedAt, &s.UpdatedAt,
		&s.StripeScheduleID, &s.ScheduledChangeAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func mapSubscriptionError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "subscriptions_company_live_uniq"):
		return model.ErrSubscriptionExists
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
