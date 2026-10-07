package stripegw

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const stripeSubJSON = `{"id":"sub_1","object":"subscription","customer":"cus_1","status":"incomplete",
	"metadata":{"eld_subscription_id":"0199a1b2-0000-7000-8000-000000000001"},
	"cancel_at_period_end":false,"canceled_at":null,
	"items":{"object":"list","data":[
		{"id":"si_a","object":"subscription_item","quantity":12,"price":{"id":"price_truck","object":"price"},
		 "current_period_start":1791331200,"current_period_end":1794009600},
		{"id":"si_b","object":"subscription_item","quantity":1,"price":{"id":"price_flat","object":"price"},
		 "current_period_start":1791331200,"current_period_end":1794009600}]},
	"latest_invoice":{"id":"in_1","object":"invoice","status":"open","amount_due":40000,
		"confirmation_secret":{"client_secret":"pi_1_secret_abc","type":"payment_intent"}}}`

func TestSubscriptions_Create(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{"POST /v1/subscriptions": {200, stripeSubJSON}})
	in := model.StripeSubscriptionCreate{
		ID: uuid.Must(uuid.NewV7()), CompanyID: uuid.New(), CustomerID: "cus_1",
		Items: []model.StripeSubscriptionItem{{StripePriceID: "price_truck", Quantity: 12}, {StripePriceID: "price_flat", Quantity: 1}},
	}

	got, err := NewSubscriptions(c).CreateSubscription(context.Background(), in)

	require.NoError(t, err)
	r := f.find(t, "POST /v1/subscriptions")
	assert.Equal(t, "cus_1", r.Form.Get("customer"))
	assert.Equal(t, "price_truck", r.Form.Get("items[0][price]"))
	assert.Equal(t, "12", r.Form.Get("items[0][quantity]"))
	assert.Equal(t, "default_incomplete", r.Form.Get("payment_behavior"), "pay first: create, do not charge")
	for _, field := range []string{"default_payment_method", "payment_settings[save_default_payment_method]"} {
		_, sent := r.Form[field]
		assert.False(t, sent, "%s: renewals must charge the customer's default card, which the company can change", field)
	}
	assert.Equal(t, "latest_invoice.confirmation_secret", r.Form.Get("expand[0]"))
	assert.Equal(t, in.ID.String(), r.Form.Get("metadata[eld_subscription_id]"))
	assert.Equal(t, in.CompanyID.String(), r.Form.Get("metadata[eld_company_id]"))
	assert.Equal(t, "subscription-create-"+in.ID.String(), r.IdempotencyKey)

	assert.Equal(t, "sub_1", got.ID)
	assert.Equal(t, "cus_1", got.CustomerID)
	assert.Equal(t, model.SubscriptionIncomplete, got.Status)
	require.Len(t, got.Items, 2)
	assert.Equal(t, model.StripeSubscriptionItem{ID: "si_a", StripePriceID: "price_truck", Quantity: 12,
		PeriodStart: ptrTime(1791331200), PeriodEnd: ptrTime(1794009600)}, got.Items[0])
	require.NotNil(t, got.LatestInvoice)
	assert.Equal(t, model.StripeInvoice{ID: "in_1", Status: "open", AmountDue: 40000, ClientSecret: "pi_1_secret_abc"},
		*got.LatestInvoice)
	assert.Nil(t, got.CanceledAt, "null becomes nil, not 1970")
}

func TestSubscriptions_Get(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{"GET /v1/subscriptions/sub_1": {200, stripeSubJSON}})
	g := NewSubscriptions(c)

	got, err := g.GetSubscription(context.Background(), "sub_1")
	require.NoError(t, err)
	assert.Equal(t, "sub_1", got.ID)
	assert.Equal(t, "latest_invoice.confirmation_secret", f.find(t, "GET /v1/subscriptions/sub_1").Form.Get("expand[0]"))

	_, err = g.GetSubscription(context.Background(), "sub_gone")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func ptrTime(sec int64) *time.Time {
	t := time.Unix(sec, 0).UTC()
	return &t
}

func TestSubscriptions_InvoiceSubscriptionID(t *testing.T) {
	_, c := newRoutedFake(t, map[string]reply{
		"GET /v1/invoices/in_sub": {200, `{"id":"in_sub","object":"invoice",
			"parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_1"}}}`},
		"GET /v1/invoices/in_oneoff": {200, `{"id":"in_oneoff","object":"invoice","parent":null}`},
	})
	g := NewSubscriptions(c)

	id, err := g.InvoiceSubscriptionID(context.Background(), "in_sub")
	require.NoError(t, err)
	assert.Equal(t, "sub_1", id)

	id, err = g.InvoiceSubscriptionID(context.Background(), "in_oneoff")
	require.NoError(t, err)
	assert.Empty(t, id, "a one-off invoice belongs to no subscription")

	_, err = g.InvoiceSubscriptionID(context.Background(), "in_gone")
	assert.ErrorIs(t, err, model.ErrNotFound)
}
