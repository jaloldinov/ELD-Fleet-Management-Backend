package stripegw

import (
	"context"
	"errors"
	"fmt"
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

var planChange = []model.StripeItemChange{
	{ID: "si_keep", Quantity: 14},                 // per-truck item, new truck count
	{ID: "si_drop", Deleted: true},                // remove support
	{StripePriceID: "price_premium", Quantity: 1}, // add premium
}

func TestSubscriptions_PreviewChange(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		// A truck added this month: its proration waits for the renewal.
		"GET /v1/invoiceitems": {200, `{"object":"list","has_more":false,"data":[{"id":"ii_truck","object":"invoiceitem"}]}`},
		"POST /v1/invoices/create_preview": {200, `{"object":"invoice","starting_balance":-1000,"lines":{"object":"list","data":[
			{"object":"line_item","description":"Unused time on Support after 7 Oct","amount":-5000,
			 "parent":{"type":"subscription_item_details","subscription_item_details":{"invoice_item":"ii_new1","proration":true}}},
			{"object":"line_item","description":"Remaining time on Premium after 7 Oct","amount":8000,
			 "parent":{"type":"subscription_item_details","subscription_item_details":{"invoice_item":"ii_new2","proration":true}}},
			{"object":"line_item","description":"Remaining time on 3 × ELD after 3 Oct","amount":7500,
			 "parent":{"type":"subscription_item_details","subscription_item_details":{"invoice_item":"ii_truck","proration":true}}}]}}`},
	})
	before := time.Now().Unix()

	got, err := NewSubscriptions(c).PreviewChange(context.Background(), "sub_1", "cus_1", planChange)

	require.NoError(t, err)
	r := f.find(t, "POST /v1/invoices/create_preview")
	assert.Equal(t, "sub_1", r.Form.Get("subscription"))
	assert.Equal(t, "cus_1", r.Form.Get("customer"))
	assert.Equal(t, "always_invoice", r.Form.Get("subscription_details[proration_behavior]"))
	assert.Equal(t, "si_keep", r.Form.Get("subscription_details[items][0][id]"))
	assert.Equal(t, "14", r.Form.Get("subscription_details[items][0][quantity]"))
	assert.Equal(t, "true", r.Form.Get("subscription_details[items][1][deleted]"))
	assert.Equal(t, "price_premium", r.Form.Get("subscription_details[items][2][price]"))
	assert.Equal(t, r.Form.Get("subscription_details[proration_date]"), fmt.Sprint(got.ProrationDate),
		"the date sent is the date returned, to confirm with")
	assert.GreaterOrEqual(t, got.ProrationDate, before)
	assert.Equal(t, "true", f.find(t, "GET /v1/invoiceitems").Form.Get("pending"))

	require.Len(t, got.Lines, 2, "the truck proration waits for the renewal: not part of this change")
	assert.Equal(t, model.PreviewLine{Description: "Unused time on Support after 7 Oct", Amount: -5000, Proration: true}, got.Lines[0])
	assert.Equal(t, int64(3000), got.Total, "-50 + 80")
	assert.Equal(t, int64(2000), got.AmountDue, "the $10 credit the customer has pays first")
}

func TestSubscriptions_PreviewChange_Downgrade(t *testing.T) {
	_, c := newRoutedFake(t, map[string]reply{
		"GET /v1/invoiceitems": {200, `{"object":"list","has_more":false,"data":[]}`},
		"POST /v1/invoices/create_preview": {200, `{"object":"invoice","starting_balance":0,"lines":{"object":"list","data":[
			{"object":"line_item","description":"Unused time on Support","amount":-10000,
			 "parent":{"type":"subscription_item_details","subscription_item_details":{"invoice_item":"ii_new","proration":true}}}]}}`},
	})

	got, err := NewSubscriptions(c).PreviewChange(context.Background(), "sub_1", "cus_1", planChange)

	require.NoError(t, err)
	assert.Equal(t, int64(-10000), got.Total, "credit for the unused time")
	assert.Equal(t, int64(0), got.AmountDue, "nothing charged")
}

func TestSubscriptions_ChangeItems_PayFirst(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/subscriptions/sub_1": {200, `{"id":"sub_1","object":"subscription","status":"active",
			"pending_update":{"expires_at":1791331200},"items":{"object":"list","data":[]},
			"latest_invoice":{"id":"in_2","object":"invoice","status":"open","amount_due":2500,
				"confirmation_secret":{"client_secret":"pi_2_secret","type":"payment_intent"}}}`},
	})

	got, err := NewSubscriptions(c).ChangeItems(context.Background(), "sub_1", planChange, 1791331200)

	require.NoError(t, err)
	r := f.find(t, "POST /v1/subscriptions/sub_1")
	assert.Equal(t, "always_invoice", r.Form.Get("proration_behavior"), "charge (or credit) the difference now")
	assert.Equal(t, "pending_if_incomplete", r.Form.Get("payment_behavior"), "pay first: not applied unless paid")
	assert.Equal(t, "1791331200", r.Form.Get("proration_date"))
	assert.Equal(t, "true", r.Form.Get("items[1][deleted]"))
	assert.True(t, got.PendingUpdate, "the bank wants 3-D Secure: the change waits for the payment")
	assert.Equal(t, "pi_2_secret", got.LatestInvoice.ClientSecret)
}

func TestSubscriptions_SetQuantities_NoChargeNow(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{"POST /v1/subscriptions/sub_1": {200, stripeSubJSON}})

	_, err := NewSubscriptions(c).SetQuantities(context.Background(), "sub_1", []model.StripeItemChange{{ID: "si_a", Quantity: 13}})

	require.NoError(t, err)
	r := f.find(t, "POST /v1/subscriptions/sub_1")
	assert.Equal(t, "create_prorations", r.Form.Get("proration_behavior"), "the difference goes on the next invoice")
	assert.Equal(t, "13", r.Form.Get("items[0][quantity]"))
	_, sent := r.Form["payment_behavior"]
	assert.False(t, sent)
}

func TestSubscriptions_Cancel(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/subscriptions/sub_1":   {200, stripeSubJSON},
		"DELETE /v1/subscriptions/sub_1": {200, stripeSubJSON},
	})
	g := NewSubscriptions(c)

	_, err := g.SetCancelAtPeriodEnd(context.Background(), "sub_1", true)
	require.NoError(t, err)
	assert.Equal(t, "true", f.find(t, "POST /v1/subscriptions/sub_1").Form.Get("cancel_at_period_end"))

	_, err = g.CancelNow(context.Background(), "sub_1")
	require.NoError(t, err)
	r := f.find(t, "DELETE /v1/subscriptions/sub_1")
	assert.Equal(t, "true", r.Form.Get("prorate"), "unused time becomes credit")
	assert.Equal(t, "true", r.Form.Get("invoice_now"))
}

func TestWrap_CardDeclinedIsForTheUser(t *testing.T) {
	_, c := newRoutedFake(t, map[string]reply{
		"POST /v1/subscriptions/sub_1": {402, `{"error":{"type":"card_error","code":"card_declined",
			"decline_code":"insufficient_funds","message":"Your card has insufficient funds."}}`},
	})

	_, err := NewSubscriptions(c).ChangeItems(context.Background(), "sub_1", planChange, 1)

	assert.ErrorIs(t, err, model.ErrCardDeclined)
	assert.False(t, errors.Is(err, model.ErrPaymentProvider), "a decline is not an outage")
	assert.Contains(t, err.Error(), "Your card has insufficient funds.")
}
