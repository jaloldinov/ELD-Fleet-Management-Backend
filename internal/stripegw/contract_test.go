package stripegw

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/stripeclient"
)

// Contract tests: every call this package makes, sent to stripe-mock,
// Stripe's official fake server. stripe-mock validates each request against
// Stripe's API specification (endpoint, parameter names and types, expand
// paths) and answers with a sample object.
//
// It keeps no state and does not simulate billing (every subscription is
// "active"), so these tests prove the SHAPE of our requests, not billing
// behavior: that is what the real-Stripe e2e suite is for. They catch, for
// example, a misspelled parameter after an SDK upgrade, offline and in
// every CI run.
//
// Needs STRIPE_MOCK_URL (e.g. http://localhost:12111); skipped without it.
//
//	docker compose up -d stripe-mock
//	STRIPE_MOCK_URL=http://localhost:12111 go test ./internal/stripegw -run Contract
func mockClient(t *testing.T) *stripe.Client {
	t.Helper()
	url := os.Getenv("STRIPE_MOCK_URL")
	if url == "" {
		t.Skip("STRIPE_MOCK_URL not set; start stripe-mock (docker compose up -d stripe-mock)")
	}
	return stripeclient.New(stripeclient.Config{SecretKey: "sk_test_123", URL: url, Log: slog.New(slog.DiscardHandler)})
}

func TestContract_Account(t *testing.T) {
	_, err := stripeclient.Check(context.Background(), mockClient(t))
	require.NoError(t, err)
}

func TestContract_Catalog(t *testing.T) {
	g := NewCatalog(mockClient(t))
	ctx := context.Background()

	_, err := g.CreateProduct(ctx, model.Product{ID: uuid.New(), Name: "ELD Fleet plan", Description: "HOS"})
	require.NoError(t, err)
	require.NoError(t, g.UpdateProduct(ctx, model.Product{StripeProductID: "prod_123", Name: "Renamed", IsActive: false}))
	require.NoError(t, g.UpdateProduct(ctx, model.Product{StripeProductID: "prod_123", Name: "No description"}), "unset description")
	_, err = g.CreatePrice(ctx, "prod_123", model.Price{ID: uuid.New(), Nickname: "Monthly", Currency: "usd",
		UnitAmount: 2500, Interval: model.IntervalMonth, BillingUnit: model.BillingUnitTruck})
	require.NoError(t, err)
	require.NoError(t, g.UpdatePrice(ctx, model.Price{StripePriceID: "price_123", Nickname: "", IsActive: false}))
}

func TestContract_Customers(t *testing.T) {
	g := NewCustomers(mockClient(t))
	ctx := context.Background()

	_, err := g.CreateCustomer(ctx, model.Company{ID: uuid.New(), Name: "Acme", DOTNumber: "1234567"})
	require.NoError(t, err)
	_, err = g.CreateSetupIntent(ctx, "cus_123")
	require.NoError(t, err)
	_, err = g.ListPaymentMethods(ctx, "cus_123")
	require.NoError(t, err)
	_, err = g.GetPaymentMethod(ctx, "pm_123")
	require.NoError(t, err)
	require.NoError(t, g.SetDefaultPaymentMethod(ctx, "cus_123", "pm_123"))
	require.NoError(t, g.DetachPaymentMethod(ctx, "pm_123"))
}

var contractItems = []model.StripeItemChange{
	{ID: "si_keep", Quantity: 4},
	{ID: "si_drop", Deleted: true},
	{StripePriceID: "price_new", Quantity: 1},
}

func TestContract_Subscriptions(t *testing.T) {
	g := NewSubscriptions(mockClient(t))
	ctx := context.Background()

	_, err := g.CreateSubscription(ctx, model.StripeSubscriptionCreate{ID: uuid.New(), CompanyID: uuid.New(), CustomerID: "cus_123",
		Items: []model.StripeSubscriptionItem{{StripePriceID: "price_123", Quantity: 3}}})
	require.NoError(t, err)
	_, err = g.GetSubscription(ctx, "sub_123")
	require.NoError(t, err)
	_, err = g.PreviewChange(ctx, "sub_123", "cus_123", contractItems)
	require.NoError(t, err)
	_, err = g.ChangeItems(ctx, "sub_123", contractItems, 1791331200)
	require.NoError(t, err)
	_, err = g.SetQuantities(ctx, "sub_123", []model.StripeItemChange{{ID: "si_keep", Quantity: 5}})
	require.NoError(t, err)
	_, err = g.SetCancelAtPeriodEnd(ctx, "sub_123", true)
	require.NoError(t, err)
	_, err = g.CancelNow(ctx, "sub_123")
	require.NoError(t, err)
	_, err = g.InvoiceSubscriptionID(ctx, "in_123")
	require.NoError(t, err)
	_, err = g.ReleaseSchedule(ctx, "sub_sched_123", "sub_123")
	require.NoError(t, err)
}

func TestContract_ScheduleChange(t *testing.T) {
	g := NewSubscriptions(mockClient(t))
	in := model.StripeScheduleChange{
		SubscriptionID: "sub_123",
		CurrentItems:   []model.StripeSubscriptionItem{{StripePriceID: "price_month", Quantity: 3}},
		NextItems:      []model.StripeSubscriptionItem{{StripePriceID: "price_year", Quantity: 3}},
		NextInterval:   model.IntervalYear,
	}

	// Create-then-update, and update of an existing schedule.
	for _, scheduleID := range []string{"", "sub_sched_123"} {
		in.ScheduleID = scheduleID
		_, err := g.ScheduleChange(context.Background(), in)
		if err != nil {
			// stripe-mock's sample schedule may have no current phase; our
			// code refuses that on purpose. Every request before it was
			// accepted by the spec, which is what this test checks.
			assert.ErrorContains(t, err, "has no current phase", "schedule=%q", scheduleID)
		}
	}
}

func TestContract_Invoices(t *testing.T) {
	g := NewInvoices(mockClient(t))
	ctx := context.Background()

	_, err := g.GetInvoice(ctx, "in_123", true)
	require.NoError(t, err)
	_, err = g.PayInvoice(ctx, "in_123")
	require.NoError(t, err)
	_, err = g.FinalizeInvoice(ctx, "in_123")
	require.NoError(t, err)
	_, err = g.VoidInvoice(ctx, "in_123")
	require.NoError(t, err)
	_, err = g.CreateOneOffCharge(ctx, model.StripeOneOffCharge{ID: uuid.New(), CompanyID: uuid.New(), CustomerID: "cus_123",
		Description: "ELD installation", Amount: 15000})
	require.NoError(t, err)
	_, err = g.Balance(ctx, "cus_123")
	require.NoError(t, err)
	_, err = g.AddCredit(ctx, "cus_123", 2500, "Outage", "credit-"+uuid.NewString())
	require.NoError(t, err)
	for _, refund := range []bool{true, false} {
		_, err = g.CreateCreditNote(ctx, model.StripeCreditNoteRequest{ID: uuid.New(), InvoiceID: "in_123", Amount: 5000,
			Paid: true, Refund: refund, Reason: "order_change", Memo: "x"})
		require.NoError(t, err, "refund=%v", refund)
	}
	_, err = g.ListCreditNotes(ctx, "in_123")
	require.NoError(t, err)
}
