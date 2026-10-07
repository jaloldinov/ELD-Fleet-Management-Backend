package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const invoiceColumns = `id, company_id, stripe_invoice_id, coalesce(stripe_subscription_id, ''), coalesce(number, ''),
	status, billing_reason, description, currency, total, amount_due, amount_paid, amount_remaining,
	coalesce(hosted_invoice_url, ''), coalesce(invoice_pdf, ''), period_start, period_end, stripe_created_at,
	finalized_at, paid_at, voided_at, created_at, updated_at`

// InvoiceRepository stores our copy of companies' Stripe invoices.
type InvoiceRepository struct {
	db DBTX
}

func NewInvoiceRepository(db DBTX) *InvoiceRepository {
	return &InvoiceRepository{db: db}
}

func (r *InvoiceRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Upsert saves the invoice as Stripe sees it (found by its Stripe id; a new
// one gets inv.ID). Webhooks can arrive out of order: an older "open" must
// not overwrite a newer "paid", so a paid or void invoice keeps its status.
func (r *InvoiceRepository) Upsert(ctx context.Context, inv model.Invoice) (*model.Invoice, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO invoices (id, company_id, stripe_invoice_id, stripe_subscription_id, number, status,
		                      billing_reason, description, currency, total, amount_due, amount_paid,
		                      amount_remaining, hosted_invoice_url, invoice_pdf, period_start, period_end,
		                      stripe_created_at, finalized_at, paid_at, voided_at)
		VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, $7, $8, $9, $10, $11, $12, $13,
		        nullif($14, ''), nullif($15, ''), $16, $17, $18, $19, $20, $21)
		ON CONFLICT (stripe_invoice_id) DO UPDATE SET
			number             = EXCLUDED.number,
			status             = CASE WHEN invoices.status IN ('paid', 'void') THEN invoices.status ELSE EXCLUDED.status END,
			description        = EXCLUDED.description,
			total              = EXCLUDED.total,
			amount_due         = EXCLUDED.amount_due,
			amount_paid        = greatest(invoices.amount_paid, EXCLUDED.amount_paid),
			amount_remaining   = CASE WHEN invoices.status IN ('paid', 'void') THEN invoices.amount_remaining ELSE EXCLUDED.amount_remaining END,
			hosted_invoice_url = EXCLUDED.hosted_invoice_url,
			invoice_pdf        = EXCLUDED.invoice_pdf,
			finalized_at       = coalesce(invoices.finalized_at, EXCLUDED.finalized_at),
			paid_at            = coalesce(invoices.paid_at, EXCLUDED.paid_at),
			voided_at          = coalesce(invoices.voided_at, EXCLUDED.voided_at)
		RETURNING `+invoiceColumns,
		inv.ID, inv.CompanyID, inv.StripeInvoiceID, inv.StripeSubscriptionID, inv.Number, inv.Status,
		inv.BillingReason, inv.Description, inv.Currency, inv.Total, inv.AmountDue, inv.AmountPaid,
		inv.AmountRemaining, inv.HostedInvoiceURL, inv.InvoicePDF, inv.PeriodStart, inv.PeriodEnd,
		inv.StripeCreatedAt, inv.FinalizedAt, inv.PaidAt, inv.VoidedAt,
	)
	saved, err := scanInvoice(row)
	if err != nil {
		return nil, mapInvoiceError("upsert invoice", err)
	}
	return saved, nil
}

func (r *InvoiceRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Invoice, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE id = $1`, id)
	inv, err := scanInvoice(row)
	if err != nil {
		return nil, mapInvoiceError("get invoice", err)
	}
	return inv, nil
}

func (r *InvoiceRepository) GetByStripeID(ctx context.Context, stripeID string) (*model.Invoice, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+invoiceColumns+` FROM invoices WHERE stripe_invoice_id = $1`, stripeID)
	inv, err := scanInvoice(row)
	if err != nil {
		return nil, mapInvoiceError("get invoice by stripe id", err)
	}
	return inv, nil
}

// List returns one page of invoices, newest first.
func (r *InvoiceRepository) List(ctx context.Context, f model.InvoiceFilter, limit, offset int) ([]model.Invoice, int, error) {
	const where = `
		WHERE ($1::uuid IS NULL OR company_id = $1)
		  AND ($2 = '' OR status = $2)`
	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM invoices`+where, f.CompanyID, f.Status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count invoices: %w", err)
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT `+invoiceColumns+` FROM invoices`+where+`
		ORDER BY stripe_created_at DESC, id DESC
		LIMIT $3 OFFSET $4`, f.CompanyID, f.Status, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list invoices: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Invoice, error) {
		inv, err := scanInvoice(row)
		if err != nil {
			return model.Invoice{}, err
		}
		return *inv, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan invoices: %w", err)
	}
	return out, total, nil
}

func scanInvoice(row pgx.Row) (*model.Invoice, error) {
	var i model.Invoice
	err := row.Scan(&i.ID, &i.CompanyID, &i.StripeInvoiceID, &i.StripeSubscriptionID, &i.Number,
		&i.Status, &i.BillingReason, &i.Description, &i.Currency, &i.Total, &i.AmountDue, &i.AmountPaid, &i.AmountRemaining,
		&i.HostedInvoiceURL, &i.InvoicePDF, &i.PeriodStart, &i.PeriodEnd, &i.StripeCreatedAt,
		&i.FinalizedAt, &i.PaidAt, &i.VoidedAt, &i.CreatedAt, &i.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func mapInvoiceError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
