package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func TestBillingMetricsRepository(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	companies := NewCompanyRepository(db)
	products := NewProductRepository(db)
	subs := NewSubscriptionRepository(db)
	invoices := NewInvoiceRepository(db)
	metrics := NewBillingMetricsRepository(db)

	eld := createProduct(t, products, "ELD Fleet plan")
	support := createProduct(t, products, "Annual support")
	perTruck := createPrice(t, products, eld.ID, 2500, model.IntervalMonth, model.BillingUnitTruck)      // $25 / truck / month
	yearly := createPrice(t, products, support.ID, 120000, model.IntervalYear, model.BillingUnitCompany) // $1,200 / year = $100 / month

	n := 0
	subscribe := func(status string, price *model.Price, qty int) *model.Company {
		n++
		c := createCompany(t, companies, fmt.Sprintf("Carrier %d", n), fmt.Sprint(n))
		s := model.Subscription{ID: uuid.Must(uuid.NewV7()), CompanyID: c.ID, StripeSubscriptionID: fmt.Sprintf("sub_%d", n),
			Status: status, Interval: price.Interval,
			Items: []model.SubscriptionItem{{StripeSubscriptionItemID: fmt.Sprintf("si_%d", n), Price: *price, Quantity: qty}}}
		if status == model.SubscriptionCanceled {
			now := time.Now()
			s.CanceledAt = &now
		}
		_, err := subs.Upsert(ctx, s)
		require.NoError(t, err)
		return c
	}
	a := subscribe(model.SubscriptionActive, perTruck, 4) // $100 / month
	subscribe(model.SubscriptionPastDue, perTruck, 2)     // $50: still collectable
	subscribe(model.SubscriptionActive, yearly, 1)        // $1,200 / 12 = $100
	subscribe(model.SubscriptionCanceled, perTruck, 10)   // ended: not revenue
	subscribe(model.SubscriptionIncomplete, perTruck, 7)  // never paid: not revenue
	_, err := companies.SetBillingStatus(ctx, a.ID, model.BillingActive)
	require.NoError(t, err)

	t.Run("MRR", func(t *testing.T) {
		got, err := metrics.MRRByProduct(ctx)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "ELD Fleet plan", got[0].ProductName)
		assert.Equal(t, int64(15000), got[0].MRR, "4 × $25 + 2 × $25 (past_due counts)")
		assert.Equal(t, 2, got[0].Subscriptions)
		assert.Equal(t, int64(10000), got[1].MRR, "a yearly price counts 1/12 per month")
	})

	t.Run("counts", func(t *testing.T) {
		byStatus, err := metrics.SubscriptionsByStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, map[string]int{"active": 2, "past_due": 1, "canceled": 1, "incomplete": 1}, byStatus)

		byBilling, err := metrics.CompaniesByBillingStatus(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, byBilling[model.BillingActive])
		assert.Equal(t, 4, byBilling[model.BillingNone])
	})

	thisMonth := time.Now().UTC()
	lastMonth := thisMonth.AddDate(0, -1, 0)
	paidInvoice := func(stripeID string, amount int64, paidAt time.Time) {
		inv := newInvoice(a.ID, stripeID, model.InvoicePaid, paidAt)
		inv.AmountPaid, inv.AmountRemaining, inv.PaidAt = amount, 0, &paidAt
		_, err := invoices.Upsert(ctx, inv)
		require.NoError(t, err)
	}
	paidInvoice("in_now1", 10000, thisMonth)
	paidInvoice("in_now2", 5000, thisMonth)
	paidInvoice("in_last", 7500, lastMonth)
	open := newInvoice(a.ID, "in_open", model.InvoiceOpen, thisMonth)
	open.AmountRemaining = 4200
	_, err = invoices.Upsert(ctx, open)
	require.NoError(t, err)
	void := newInvoice(a.ID, "in_void", model.InvoiceVoid, thisMonth)
	_, err = invoices.Upsert(ctx, void)
	require.NoError(t, err)

	t.Run("outstanding", func(t *testing.T) {
		count, amount, err := metrics.Outstanding(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "only open invoices; void is not owed")
		assert.Equal(t, int64(4200), amount)
	})

	t.Run("payment failures", func(t *testing.T) {
		_, err := db.Exec(ctx, `INSERT INTO stripe_events (id, type, received_at) VALUES
			('evt_f1', 'invoice.payment_failed', now() - interval '2 days'),
			('evt_f2', 'invoice.payment_failed', now() - interval '20 days'),
			('evt_f3', 'invoice.payment_failed', now() - interval '45 days'),
			('evt_ok', 'invoice.paid',           now())`)
		require.NoError(t, err)
		n, err := metrics.PaymentFailuresSince(ctx, time.Now().AddDate(0, 0, -30))
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})

	t.Run("monthly", func(t *testing.T) {
		got, err := metrics.Monthly(ctx, lastMonth.AddDate(0, 0, -lastMonth.Day()+1))
		require.NoError(t, err)
		now := got[thisMonth.Format("2006-01")]
		assert.Equal(t, int64(15000), now.Revenue)
		assert.Equal(t, 4, now.NewSubscriptions, "started and paid at least once (incomplete excluded)")
		assert.Equal(t, 1, now.CanceledSubscriptions)
		assert.Equal(t, int64(7500), got[lastMonth.Format("2006-01")].Revenue)
	})
}
