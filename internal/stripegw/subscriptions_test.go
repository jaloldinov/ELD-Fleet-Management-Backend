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

// A subscription with an attached schedule: monthly until 1794049978, then
// yearly × 3.
const scheduledSubJSON = `{"id":"sub_1","object":"subscription","customer":"cus_1","status":"active",
	"items":{"object":"list","data":[{"id":"si_a","object":"subscription_item","quantity":3,
		"price":{"id":"price_month","object":"price"},"current_period_start":1791371578,"current_period_end":1794049978}]},
	"schedule":{"id":"sub_sched_1","object":"subscription_schedule","status":"active",
		"current_phase":{"start_date":1791371583,"end_date":1794049978},
		"phases":[
			{"start_date":1791371583,"end_date":1794049978,"items":[{"price":"price_month","quantity":3}]},
			{"start_date":1794049978,"end_date":1825585978,"items":[{"price":"price_year","quantity":3}]}]}}`

func TestSubscriptions_ReadsTheSchedule(t *testing.T) {
	_, c := newRoutedFake(t, map[string]reply{"GET /v1/subscriptions/sub_1": {200, scheduledSubJSON}})

	got, err := NewSubscriptions(c).GetSubscription(context.Background(), "sub_1")

	require.NoError(t, err)
	require.NotNil(t, got.Schedule)
	assert.Equal(t, "sub_sched_1", got.Schedule.ID)
	assert.Equal(t, int64(1791371583), got.Schedule.CurrentPhaseStart)
	require.NotNil(t, got.Schedule.NextAt)
	assert.Equal(t, int64(1794049978), got.Schedule.NextAt.Unix())
	assert.Equal(t, []model.StripeSubscriptionItem{{StripePriceID: "price_year", Quantity: 3}}, got.Schedule.NextItems)
}

func TestSubscriptions_ExpandsTheScheduleEverywhere(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"GET /v1/subscriptions/sub_1":  {200, scheduledSubJSON},
		"POST /v1/subscriptions/sub_1": {200, scheduledSubJSON},
	})
	g := NewSubscriptions(c)

	_, err := g.GetSubscription(context.Background(), "sub_1")
	require.NoError(t, err)
	_, err = g.SetQuantities(context.Background(), "sub_1", []model.StripeItemChange{{ID: "si_a", Quantity: 4}})
	require.NoError(t, err)

	for _, call := range []string{"GET /v1/subscriptions/sub_1", "POST /v1/subscriptions/sub_1"} {
		assert.Equal(t, "schedule", f.find(t, call).Form.Get("expand[1]"),
			"%s: without it, a sync would erase the scheduled change", call)
	}
}

func TestSubscriptions_ScheduleChange_CreatesThenSetsPhases(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/subscription_schedules": {200, `{"id":"sub_sched_1","object":"subscription_schedule"}`},
		"GET /v1/subscription_schedules/sub_sched_1": {200, `{"id":"sub_sched_1","object":"subscription_schedule","status":"active",
			"current_phase":{"start_date":1791371583,"end_date":1794049978}}`},
		"POST /v1/subscription_schedules/sub_sched_1": {200, `{"id":"sub_sched_1","object":"subscription_schedule"}`},
		"GET /v1/subscriptions/sub_1":                 {200, scheduledSubJSON},
	})

	got, err := NewSubscriptions(c).ScheduleChange(context.Background(), model.StripeScheduleChange{
		SubscriptionID: "sub_1",
		CurrentItems:   []model.StripeSubscriptionItem{{StripePriceID: "price_month", Quantity: 3}},
		NextItems:      []model.StripeSubscriptionItem{{StripePriceID: "price_year", Quantity: 3}},
		NextInterval:   model.IntervalYear,
	})

	require.NoError(t, err)
	assert.Equal(t, "sub_1", f.find(t, "POST /v1/subscription_schedules").Form.Get("from_subscription"))
	u := f.find(t, "POST /v1/subscription_schedules/sub_sched_1").Form
	assert.Equal(t, "release", u.Get("end_behavior"), "after the new plan's first period, the subscription runs on normally")
	assert.Equal(t, "none", u.Get("proration_behavior"), "switching exactly at the renewal: nothing to prorate")
	assert.Equal(t, "1791371583", u.Get("phases[0][start_date]"), "from the CURRENT phase's real start")
	assert.Equal(t, "1794049978", u.Get("phases[0][end_date]"))
	assert.Equal(t, "price_month", u.Get("phases[0][items][0][price]"))
	assert.Equal(t, "price_year", u.Get("phases[1][items][0][price]"))
	assert.Equal(t, "3", u.Get("phases[1][items][0][quantity]"))
	assert.Equal(t, "year", u.Get("phases[1][duration][interval]"))
	assert.Equal(t, "1", u.Get("phases[1][duration][interval_count]"))
	require.NotNil(t, got.Schedule, "returns the subscription as it is now")
}

func TestSubscriptions_ScheduleChange_ReusesTheSchedule(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"GET /v1/subscription_schedules/sub_sched_1": {200, `{"id":"sub_sched_1","object":"subscription_schedule","status":"active",
			"current_phase":{"start_date":1,"end_date":2}}`},
		"POST /v1/subscription_schedules/sub_sched_1": {200, `{"id":"sub_sched_1","object":"subscription_schedule"}`},
		"GET /v1/subscriptions/sub_1":                 {200, scheduledSubJSON},
	})

	_, err := NewSubscriptions(c).ScheduleChange(context.Background(), model.StripeScheduleChange{
		SubscriptionID: "sub_1", ScheduleID: "sub_sched_1", NextInterval: model.IntervalMonth,
	})

	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		assert.NotEqual(t, "POST /v1/subscription_schedules", r.Method+" "+r.Path, "no second schedule")
	}
}

func TestSubscriptions_ReleaseSchedule(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/subscription_schedules/sub_sched_1/release": {200, `{"id":"sub_sched_1","object":"subscription_schedule","status":"released"}`},
		"GET /v1/subscriptions/sub_1":                         {200, stripeSubJSON},
	})

	got, err := NewSubscriptions(c).ReleaseSchedule(context.Background(), "sub_sched_1", "sub_1")

	require.NoError(t, err)
	f.find(t, "POST /v1/subscription_schedules/sub_sched_1/release")
	assert.Nil(t, got.Schedule)
}
