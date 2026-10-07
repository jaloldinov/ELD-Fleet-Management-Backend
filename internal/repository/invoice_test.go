package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func newInvoice(companyID uuid.UUID, stripeID, status string, created time.Time) model.Invoice {
	return model.Invoice{
		ID: uuid.Must(uuid.NewV7()), CompanyID: companyID, StripeInvoiceID: stripeID, StripeSubscriptionID: "sub_1",
		Status: status, BillingReason: "subscription_cycle", Currency: "usd",
		Total: 15000, AmountDue: 15000, AmountRemaining: 15000, StripeCreatedAt: created,
	}
}

func TestInvoiceRepository_UpsertAndRead(t *testing.T) {
	db := newTestDB(t)
	c := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewInvoiceRepository(db)
	ctx := context.Background()
	created := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	in := newInvoice(c.ID, "in_1", model.InvoiceOpen, created)
	in.Number, in.HostedInvoiceURL = "ABCD-0001", "https://invoice.stripe.com/i/x"
	saved, err := repo.Upsert(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, in.ID, saved.ID)
	assert.Equal(t, "ABCD-0001", saved.Number)
	assert.True(t, created.Equal(saved.StripeCreatedAt))

	// Paid: updated in place, our id kept.
	paidAt := created.Add(time.Minute)
	in.ID, in.Status, in.AmountPaid, in.AmountRemaining, in.PaidAt = uuid.New(), model.InvoicePaid, 15000, 0, &paidAt
	saved, err = repo.Upsert(ctx, in)
	require.NoError(t, err)
	assert.NotEqual(t, in.ID, saved.ID, "the existing row is updated, not replaced")
	assert.Equal(t, model.InvoicePaid, saved.Status)

	got, err := repo.GetByStripeID(ctx, "in_1")
	require.NoError(t, err)
	assert.Equal(t, saved.ID, got.ID)
	assert.Equal(t, "sub_1", got.StripeSubscriptionID)
}

func TestInvoiceRepository_LateEventCannotUnpay(t *testing.T) {
	db := newTestDB(t)
	c := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewInvoiceRepository(db)
	ctx := context.Background()
	now := time.Now()

	paid := newInvoice(c.ID, "in_1", model.InvoicePaid, now)
	paid.AmountPaid, paid.AmountRemaining, paid.PaidAt = 15000, 0, &now
	_, err := repo.Upsert(ctx, paid)
	require.NoError(t, err)

	// invoice.finalized ("open") delivered AFTER invoice.paid.
	late := newInvoice(c.ID, "in_1", model.InvoiceOpen, now)
	got, err := repo.Upsert(ctx, late)
	require.NoError(t, err)
	assert.Equal(t, model.InvoicePaid, got.Status, "a paid invoice stays paid")
	assert.Equal(t, int64(15000), got.AmountPaid)
	assert.Equal(t, int64(0), got.AmountRemaining)
	require.NotNil(t, got.PaidAt)
}

func TestInvoiceRepository_List(t *testing.T) {
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	a := createCompany(t, companies, "Acme", "1")
	b := createCompany(t, companies, "Bolt", "2")
	repo := NewInvoiceRepository(db)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	for i, inv := range []model.Invoice{
		newInvoice(a.ID, "in_a1", model.InvoicePaid, t0),
		newInvoice(a.ID, "in_a2", model.InvoiceOpen, t0.AddDate(0, 1, 0)),
		newInvoice(b.ID, "in_b1", model.InvoiceOpen, t0.AddDate(0, 0, 5)),
	} {
		_, err := repo.Upsert(ctx, inv)
		require.NoError(t, err, i)
	}

	all, total, err := repo.List(ctx, model.InvoiceFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Equal(t, "in_a2", all[0].StripeInvoiceID, "newest first")

	open, total, err := repo.List(ctx, model.InvoiceFilter{CompanyID: &a.ID, Status: model.InvoiceOpen}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, "in_a2", open[0].StripeInvoiceID)

	_, err = repo.GetByID(ctx, uuid.New())
	assert.ErrorIs(t, err, model.ErrNotFound)
}
