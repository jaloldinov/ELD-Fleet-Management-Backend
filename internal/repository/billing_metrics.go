package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// BillingMetricsRepository answers "how is the business doing" from our
// copies of subscriptions, invoices and Stripe events: plain SQL, no
// Stripe calls, so the dashboard is fast and cheap.
type BillingMetricsRepository struct {
	db DBTX
}

func NewBillingMetricsRepository(db DBTX) *BillingMetricsRepository {
	return &BillingMetricsRepository{db: db}
}

func (r *BillingMetricsRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// mrrExpr is one item's monthly value in cents: yearly prices count as a
// twelfth of their amount per month.
const mrrExpr = `CASE p.billing_interval
		WHEN 'year' THEN round(p.unit_amount * si.quantity / 12.0)
		ELSE p.unit_amount * si.quantity
	END`

// MRR per product, over subscriptions that are paid up (active) or still
// collectable (past_due). Trials pay nothing yet; unpaid, canceled and
// never-paid ones are not revenue.
func (r *BillingMetricsRepository) MRRByProduct(ctx context.Context) ([]model.ProductMRR, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT pr.id, pr.name, sum(`+mrrExpr+`)::bigint AS mrr, count(DISTINCT s.id)
		FROM subscriptions s
		JOIN subscription_items si ON si.subscription_id = s.id
		JOIN prices p             ON p.id = si.price_id
		JOIN products pr          ON pr.id = p.product_id
		WHERE s.status IN ('active', 'past_due')
		GROUP BY pr.id, pr.name
		ORDER BY mrr DESC, pr.name`)
	if err != nil {
		return nil, fmt.Errorf("mrr by product: %w", err)
	}
	defer rows.Close()
	out := []model.ProductMRR{}
	for rows.Next() {
		var m model.ProductMRR
		if err := rows.Scan(&m.ProductID, &m.ProductName, &m.MRR, &m.Subscriptions); err != nil {
			return nil, fmt.Errorf("scan mrr: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SubscriptionsByStatus counts every subscription by its status (ended
// ones included: they show churn and failed starts).
func (r *BillingMetricsRepository) SubscriptionsByStatus(ctx context.Context) (map[string]int, error) {
	return r.countBy(ctx, `SELECT status, count(*) FROM subscriptions GROUP BY status`)
}

// CompaniesByBillingStatus counts alive companies by billing status.
func (r *BillingMetricsRepository) CompaniesByBillingStatus(ctx context.Context) (map[string]int, error) {
	return r.countBy(ctx, `SELECT billing_status, count(*) FROM companies WHERE deleted_at IS NULL GROUP BY billing_status`)
}

// Outstanding is what open invoices still ask for.
func (r *BillingMetricsRepository) Outstanding(ctx context.Context) (invoices int, amount int64, err error) {
	err = r.q(ctx).QueryRow(ctx, `
		SELECT count(*), coalesce(sum(amount_remaining), 0)::bigint FROM invoices WHERE status = 'open'`).
		Scan(&invoices, &amount)
	if err != nil {
		return 0, 0, fmt.Errorf("outstanding: %w", err)
	}
	return invoices, amount, nil
}

// PaymentFailuresSince counts failed payment attempts Stripe told us about
// (each retry of the same invoice counts: it is a signal, not a sum).
func (r *BillingMetricsRepository) PaymentFailuresSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := r.q(ctx).QueryRow(ctx, `
		SELECT count(*) FROM stripe_events WHERE type = 'invoice.payment_failed' AND received_at >= $1`, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("payment failures: %w", err)
	}
	return n, nil
}

// Monthly returns, per calendar month (UTC, "2026-10") since `since`:
// revenue collected (paid invoices, by payment date, gross of refunds),
// subscriptions started (paid at least once: never-paid ones excluded) and
// subscriptions ended. Months with nothing are absent; the caller fills
// them in.
func (r *BillingMetricsRepository) Monthly(ctx context.Context, since time.Time) (map[string]model.MonthMetrics, error) {
	out := map[string]model.MonthMetrics{}

	add := func(sql string, set func(*model.MonthMetrics, int64)) error {
		rows, err := r.q(ctx).Query(ctx, sql, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var month string
			var v int64
			if err := rows.Scan(&month, &v); err != nil {
				return err
			}
			m := out[month]
			m.Month = month
			set(&m, v)
			out[month] = m
		}
		return rows.Err()
	}

	if err := add(`
		SELECT to_char(paid_at AT TIME ZONE 'UTC', 'YYYY-MM'), sum(amount_paid)::bigint
		FROM invoices WHERE status = 'paid' AND paid_at >= $1 GROUP BY 1`,
		func(m *model.MonthMetrics, v int64) { m.Revenue = v }); err != nil {
		return nil, fmt.Errorf("monthly revenue: %w", err)
	}
	if err := add(`
		SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM'), count(*)
		FROM subscriptions WHERE created_at >= $1 AND status NOT IN ('incomplete', 'incomplete_expired') GROUP BY 1`,
		func(m *model.MonthMetrics, v int64) { m.NewSubscriptions = int(v) }); err != nil {
		return nil, fmt.Errorf("monthly new subscriptions: %w", err)
	}
	if err := add(`
		SELECT to_char(canceled_at AT TIME ZONE 'UTC', 'YYYY-MM'), count(*)
		FROM subscriptions WHERE canceled_at >= $1 GROUP BY 1`,
		func(m *model.MonthMetrics, v int64) { m.CanceledSubscriptions = int(v) }); err != nil {
		return nil, fmt.Errorf("monthly canceled subscriptions: %w", err)
	}
	return out, nil
}

func (r *BillingMetricsRepository) countBy(ctx context.Context, sql string) (map[string]int, error) {
	rows, err := r.q(ctx).Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("count: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		out[k] = n
	}
	return out, rows.Err()
}
