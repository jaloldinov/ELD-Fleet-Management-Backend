package e2e

// Billing end to end, against REAL Stripe in test mode.
//
// These tests drive the billing API server like the browser and the
// platform would, pay like Stripe.js would (with the publishable key), and
// ring the webhook "doorbell" the way Stripe does (signed events about real
// Stripe objects; the API then reads the objects from Stripe). Renewals use
// Stripe test clocks: a fake calendar per customer that we move forward.
//
// Opt-in, because they are slow (Stripe round trips, clocks) and need the
// network:
//
//	make test-e2e-stripe     (E2E_STRIPE=1, Stripe TEST keys from .env)
//
// Everything they create in Stripe is named "e2e ..."; customers are
// deleted (or their test clock is) when each test ends.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- Stripe, called directly ----------

func needStripe(t *testing.T) {
	t.Helper()
	if billingURL == "" {
		t.Skip("real-Stripe billing tests are opt-in: make test-e2e-stripe (E2E_STRIPE=1 with Stripe TEST keys)")
	}
}

type obj = map[string]any

// stripeCall calls the Stripe API directly: with the publishable key it
// does what Stripe.js does in the browser; with the secret key what the
// Dashboard (or a test helper) does.
func stripeCall(t *testing.T, method, path string, form url.Values, key string) obj {
	t.Helper()
	var body io.Reader
	u := "https://api.stripe.com/v1" + path
	if method == http.MethodGet || method == http.MethodDelete {
		if len(form) > 0 {
			u += "?" + form.Encode()
		}
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, u, body)
	require.NoError(t, err)
	req.SetBasicAuth(key, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out obj
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	if resp.StatusCode >= 300 {
		t.Fatalf("stripe %s %s: %d %v", method, path, resp.StatusCode, out["error"])
	}
	return out
}

func secretKey() string      { return os.Getenv("STRIPE_SECRET_KEY") }
func publishableKey() string { return os.Getenv("STRIPE_PUBLISHABLE_KEY") }

// str and num read nested JSON fields: str(m, "payment", "client_secret").
func str(m obj, path ...string) string {
	v := dig(m, path...)
	s, _ := v.(string)
	return s
}

func num(m obj, path ...string) int64 {
	v := dig(m, path...)
	f, _ := v.(float64)
	return int64(f)
}

func dig(m obj, path ...string) any {
	var v any = m
	for _, p := range path {
		mm, ok := v.(obj)
		if !ok {
			return nil
		}
		v = mm[p]
	}
	return v
}

// send is client.do with the response decoded as a map.
func (c client) json(t *testing.T, method, path string, body any, want int) obj {
	t.Helper()
	var out obj
	r := c.do(t, method, path, body, want, nil)
	if len(r.Body) > 0 {
		r.decode(t, &out)
	}
	return out
}

// ---------- the webhook doorbell ----------

// doorbell sends a webhook signed like Stripe's: "t=<unix>,v1=HMAC-SHA256
// (secret, "<t>.<payload>")". It names a REAL Stripe object; the API reads
// that object from Stripe itself. The API handles it synchronously, so the
// effect is visible as soon as this returns.
func doorbell(t *testing.T, eventType, objectID, objectType string) {
	t.Helper()
	payload := fmt.Sprintf(`{"id":"evt_e2e%d","object":"event","type":%q,"livemode":false,`+
		`"api_version":"2026-09-30.endive","data":{"object":{"id":%q,"object":%q}}}`,
		time.Now().UnixNano(), eventType, objectID, objectType)
	ts := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(ts + "." + payload))
	req, err := http.NewRequest(http.MethodPost, billingURL+"/api/v1/webhooks/stripe", strings.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t="+ts+",v1="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "webhook %s: %s", eventType, b)
}

// ---------- fixtures ----------

// catalog is created once per run, through the API, by the platform admin.
var (
	catalogOnce sync.Once
	cat         struct{ perTruck, perTruckYearly, support string }
)

func catalog(t *testing.T) {
	t.Helper()
	catalogOnce.Do(func() {
		p := platform(t).on(billingURL)
		product := func(name string) string {
			return str(p.json(t, http.MethodPost, "/api/v1/products", obj{"name": name}, http.StatusCreated), "id")
		}
		price := func(productID string, amount int, interval, unit string) string {
			return str(p.json(t, http.MethodPost, "/api/v1/products/"+productID+"/prices",
				obj{"unit_amount": amount, "interval": interval, "billing_unit": unit}, http.StatusCreated), "id")
		}
		eld := product("e2e ELD Fleet plan")
		support := product("e2e Premium support")
		cat.perTruck = price(eld, 2500, "month", "truck")       // $25 / truck / month
		cat.perTruckYearly = price(eld, 25000, "year", "truck") // $250 / truck / year
		cat.support = price(support, 10000, "month", "company") // $100 / month
	})
	require.NotEmpty(t, cat.support, "catalog setup failed earlier")
}

// billed is a tenant whose admin talks to the billing server.
type billed struct {
	tenant
	B     client // the company admin, on the billing server
	clock string // test clock id, if the customer lives on one
}

// newBilled onboards a company with a default card. With a test clock,
// its Stripe customer lives on that clock (needed to move time).
func newBilled(t *testing.T, withClock bool) billed {
	t.Helper()
	catalog(t)
	tn := newTenant(t)
	b := billed{tenant: tn, B: tn.Admin.on(billingURL)}

	if withClock {
		b.clock = str(stripeCall(t, http.MethodPost, "/test_helpers/test_clocks",
			url.Values{"frozen_time": {fmt.Sprint(time.Now().Unix())}, "name": {"e2e"}}, secretKey()), "id")
		t.Cleanup(func() { stripeCall(t, http.MethodDelete, "/test_helpers/test_clocks/"+b.clock, nil, secretKey()) })
		customer := str(stripeCall(t, http.MethodPost, "/customers",
			url.Values{"name": {"e2e clock customer"}, "test_clock": {b.clock}}, secretKey()), "id")
		// Test-only shortcut: the API creates customers without a clock, so
		// link this one the way EnsureCustomer would have.
		_, err := testDB.Exec(t.Context(), `UPDATE companies SET stripe_customer_id = $2 WHERE id = $1`, tn.ID, customer)
		require.NoError(t, err)
	}

	b.addCard(t, "pm_card_visa")
	if !withClock {
		// Deleting the customer ends its subscriptions too.
		var customer string
		require.NoError(t, testDB.QueryRow(t.Context(), `SELECT stripe_customer_id FROM companies WHERE id = $1`, tn.ID).Scan(&customer))
		t.Cleanup(func() { stripeCall(t, http.MethodDelete, "/customers/"+customer, nil, secretKey()) })
	}
	return b
}

func (b billed) path(rest string) string { return "/api/v1/companies/" + b.ID + "/billing" + rest }

// addCard saves a Stripe test card the way the page does (SetupIntent from
// our API, confirmed by "Stripe.js") and makes it the default.
func (b billed) addCard(t *testing.T, testCard string) string {
	t.Helper()
	secret := str(b.B.json(t, http.MethodPost, b.path("/setup-intent"), nil, http.StatusCreated), "client_secret")
	si := strings.SplitN(secret, "_secret_", 2)[0]
	pm := str(stripeCall(t, http.MethodPost, "/setup_intents/"+si+"/confirm",
		url.Values{"client_secret": {secret}, "payment_method": {testCard}}, publishableKey()), "payment_method")
	require.NotEmpty(t, pm)
	b.B.do(t, http.MethodPut, b.path("/payment-methods/"+pm+"/default"), nil, http.StatusNoContent, nil)
	return pm
}

// subscribe starts a subscription and pays its first invoice like Stripe.js.
func (b billed) subscribe(t *testing.T, priceIDs ...string) obj {
	t.Helper()
	st := b.B.json(t, http.MethodPost, b.path("/subscription"), obj{"price_ids": priceIDs}, http.StatusCreated)
	if secret := str(st, "payment", "client_secret"); secret != "" {
		pi := strings.SplitN(secret, "_secret_", 2)[0]
		paid := stripeCall(t, http.MethodPost, "/payment_intents/"+pi+"/confirm",
			url.Values{"client_secret": {secret}, "payment_method": {str(st, "payment", "payment_method_id")}}, publishableKey())
		require.Equal(t, "succeeded", str(paid, "status"))
	}
	return st
}

func (b billed) billingStatus(t *testing.T) string {
	t.Helper()
	return str(b.B.json(t, http.MethodGet, "/api/v1/companies/"+b.ID, nil, http.StatusOK), "billing_status")
}

func (b billed) subscription(t *testing.T) obj {
	t.Helper()
	return b.B.json(t, http.MethodGet, b.path("/subscription"), nil, http.StatusOK)
}

// latestInvoice is the Stripe id of the subscription's newest invoice.
func latestInvoice(t *testing.T, stripeSubID string) string {
	t.Helper()
	return str(stripeCall(t, http.MethodGet, "/subscriptions/"+stripeSubID, nil, secretKey()), "latest_invoice")
}

// advanceClock moves the test clock to `to` and waits until Stripe has run
// everything that happens meanwhile (renewals, payment attempts).
func advanceClock(t *testing.T, clock string, to time.Time) {
	t.Helper()
	stripeCall(t, http.MethodPost, "/test_helpers/test_clocks/"+clock+"/advance",
		url.Values{"frozen_time": {fmt.Sprint(to.Unix())}}, secretKey())
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if str(stripeCall(t, http.MethodGet, "/test_helpers/test_clocks/"+clock, nil, secretKey()), "status") == "ready" {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("test clock did not finish advancing")
}

func balance(t *testing.T, b billed) int64 {
	t.Helper()
	return num(b.B.json(t, http.MethodGet, b.path("/balance"), nil, http.StatusOK), "balance")
}

// ---------- the stories ----------

// Pay first, then use; Stripe's webhook unlocks the fleet; the truck count
// follows the fleet; the invoice arrives in our copy.
func TestStripe_PayFirstThenUse(t *testing.T) {
	needStripe(t)
	b := newBilled(t, false)

	e := b.B.fail(t, http.MethodPost, "/api/v1/trucks", obj{"unit_number": "101", "vin": newVIN(t)}, http.StatusPaymentRequired)
	assert.Equal(t, "billing_inactive", e.Code, "no subscription: the fleet is locked")

	st := b.B.json(t, http.MethodPost, b.path("/subscription"), obj{"price_ids": []string{cat.perTruck, cat.support}}, http.StatusCreated)
	assert.Equal(t, "incomplete", str(st, "subscription", "status"))
	assert.Equal(t, int64(10000), num(st, "payment", "amount_due"), "0 trucks × $25 + $100")
	assert.Equal(t, "pending", b.billingStatus(t), "subscribed, not paid: still locked")

	// Pay like Stripe.js, then Stripe rings: invoice.paid.
	secret := str(st, "payment", "client_secret")
	stripeCall(t, http.MethodPost, "/payment_intents/"+strings.SplitN(secret, "_secret_", 2)[0]+"/confirm",
		url.Values{"client_secret": {secret}, "payment_method": {str(st, "payment", "payment_method_id")}}, publishableKey())
	stripeSub := str(st, "subscription", "stripe_subscription_id")
	doorbell(t, "invoice.paid", latestInvoice(t, stripeSub), "invoice")

	assert.Equal(t, "active", b.billingStatus(t))
	b.B.do(t, http.MethodPost, "/api/v1/trucks", obj{"unit_number": "101", "vin": newVIN(t)}, http.StatusCreated, nil)

	// The new truck reached Stripe's subscription by itself.
	for _, it := range stripeCall(t, http.MethodGet, "/subscriptions/"+stripeSub, nil, secretKey())["items"].(obj)["data"].([]any) {
		item := it.(obj)
		if str(item, "price", "id") != "" && num(item, "price", "unit_amount") == 2500 {
			assert.Equal(t, int64(1), num(item, "quantity"), "per-truck quantity follows the fleet")
		}
	}

	invoices := b.B.json(t, http.MethodGet, "/api/v1/invoices", nil, http.StatusOK)
	require.Equal(t, int64(1), num(invoices, "total"), "the paid first invoice, synced from the webhook")
	first := invoices["items"].([]any)[0].(obj)
	assert.Equal(t, "paid", str(first, "status"))
	assert.Equal(t, int64(10000), num(first, "amount_paid"))
}

// A plan change charges (or credits) exactly what its preview said.
func TestStripe_ProrationQuoteEqualsTheCharge(t *testing.T) {
	needStripe(t)
	b := newBilled(t, false)
	b.subscribe(t, cat.perTruck, cat.support)
	b.B.do(t, http.MethodPost, b.path("/subscription/refresh"), nil, http.StatusOK, nil)
	// A truck: its proration waits for the renewal and must not leak into
	// the quotes below.
	b.B.do(t, http.MethodPost, "/api/v1/trucks", obj{"unit_number": "101", "vin": newVIN(t)}, http.StatusCreated, nil)

	change := func(prices ...string) (quote obj) {
		quote = b.B.json(t, http.MethodPost, b.path("/subscription/preview"), obj{"price_ids": prices}, http.StatusOK)
		b.B.do(t, http.MethodPut, b.path("/subscription"),
			obj{"price_ids": prices, "proration_date": num(quote, "proration_date")}, http.StatusOK, nil)
		return quote
	}

	// Downgrade: drop Support.
	before := balance(t, b)
	q := change(cat.perTruck)
	assert.Equal(t, int64(0), num(q, "amount_due"))
	require.Positive(t, num(q, "credit"))
	assert.Equal(t, num(q, "credit"), before-balance(t, b), "the credit added = the credit quoted")

	// Upgrade to yearly: the credit pays first, the card the rest.
	q = change(cat.perTruckYearly)
	require.Positive(t, num(q, "amount_due"))
	var customer string
	require.NoError(t, testDB.QueryRow(t.Context(), `SELECT stripe_customer_id FROM companies WHERE id = $1`, b.ID).Scan(&customer))
	last := stripeCall(t, http.MethodGet, "/invoices", url.Values{"customer": {customer}, "limit": {"1"}}, secretKey())["data"].([]any)[0].(obj)
	assert.Equal(t, num(q, "amount_due"), num(last, "amount_paid"), "the card was charged exactly the quote")
	assert.Equal(t, "year", str(b.subscription(t), "interval"))
}

// A renewal fails: grace period (still allowed), then the company pays the
// open invoice itself and is active again.
func TestStripe_RenewalFailsThenThePaymentUnblocks(t *testing.T) {
	needStripe(t)
	b := newBilled(t, true)
	st := b.subscribe(t, cat.support)
	b.B.do(t, http.MethodPost, b.path("/subscription/refresh"), nil, http.StatusOK, nil)
	require.Equal(t, "active", b.billingStatus(t))

	b.addCard(t, "pm_card_chargeCustomerFail") // the bank declines this one
	periodEnd, err := time.Parse(time.RFC3339, str(b.subscription(t), "current_period_end"))
	require.NoError(t, err)
	advanceClock(t, b.clock, periodEnd.Add(2*time.Hour))

	stripeSub := str(st, "subscription", "stripe_subscription_id")
	renewal := latestInvoice(t, stripeSub)
	doorbell(t, "invoice.payment_failed", renewal, "invoice")

	assert.Equal(t, "past_due", b.billingStatus(t))
	r := b.B.send(t, http.MethodGet, "/api/v1/trucks", nil)
	r.require(t, http.StatusOK)
	assert.Equal(t, "past_due", r.Header.Get("X-Billing-Status"), "grace period: allowed, and the page can warn")

	open := b.B.json(t, http.MethodGet, "/api/v1/invoices?status=open", nil, http.StatusOK)
	require.Equal(t, int64(1), num(open, "total"), "the failed renewal is in our copy")
	invoiceID := str(open["items"].([]any)[0].(obj), "id")

	e := b.B.fail(t, http.MethodPost, "/api/v1/invoices/"+invoiceID+"/pay", nil, http.StatusPaymentRequired)
	assert.Equal(t, "card_declined", e.Code)

	b.addCard(t, "pm_card_visa")
	paid := b.B.json(t, http.MethodPost, "/api/v1/invoices/"+invoiceID+"/pay", nil, http.StatusOK)
	assert.Equal(t, "paid", str(paid, "invoice", "status"))
	assert.Equal(t, "active", b.billingStatus(t), "paying refreshed the subscription at once")
}

// A change scheduled for the renewal happens at the renewal, by itself.
func TestStripe_ScheduledChangeHappensAtRenewal(t *testing.T) {
	needStripe(t)
	b := newBilled(t, true)
	st := b.subscribe(t, cat.support)
	b.B.do(t, http.MethodPost, b.path("/subscription/refresh"), nil, http.StatusOK, nil)

	sub := b.B.json(t, http.MethodPut, b.path("/subscription/schedule"), obj{"price_ids": []string{cat.perTruckYearly}}, http.StatusOK)
	assert.Equal(t, "month", str(sub, "interval"), "nothing changes today")
	assert.Equal(t, "year", str(sub, "scheduled_change", "interval"))
	assert.Equal(t, str(sub, "current_period_end"), str(sub, "scheduled_change", "at"), "it starts at the renewal")

	e := b.B.fail(t, http.MethodPut, b.path("/subscription"), obj{"price_ids": []string{cat.perTruck}}, http.StatusBadRequest)
	assert.Contains(t, e.Details["subscription"], "scheduled")

	periodEnd, err := time.Parse(time.RFC3339, str(sub, "current_period_end"))
	require.NoError(t, err)
	advanceClock(t, b.clock, periodEnd.Add(2*time.Hour))
	doorbell(t, "customer.subscription.updated", str(st, "subscription", "stripe_subscription_id"), "subscription")

	after := b.subscription(t)
	assert.Equal(t, "year", str(after, "interval"), "switched at the renewal")
	assert.Nil(t, after["scheduled_change"])
}

// One-off charges, credit and refunds, as the platform does them.
func TestStripe_ChargesCreditsAndRefunds(t *testing.T) {
	needStripe(t)
	b := newBilled(t, false)
	p := platform(t).on(billingURL)

	charge := p.json(t, http.MethodPost, b.path("/charges"),
		obj{"description": "e2e ELD installation", "amount": 15000, "charge_now": true}, http.StatusCreated)
	assert.Equal(t, "paid", str(charge, "invoice", "status"))
	assert.Equal(t, "manual", str(charge, "invoice", "billing_reason"))
	invoiceID := str(charge, "invoice", "id")
	detail := b.B.json(t, http.MethodGet, "/api/v1/invoices/"+invoiceID, nil, http.StatusOK)
	require.Len(t, detail["lines"], 1, "only the charge's own line")

	note := p.json(t, http.MethodPost, "/api/v1/invoices/"+invoiceID+"/credit-notes",
		obj{"amount": 5000, "refund": true, "reason": "order_change"}, http.StatusCreated)
	assert.Equal(t, int64(5000), num(note, "refund_amount"), "back to the card")

	// A charge not collected now stays open, and can be voided.
	unpaid := p.json(t, http.MethodPost, b.path("/charges"),
		obj{"description": "e2e mistake", "amount": 1000, "charge_now": false}, http.StatusCreated)
	unpaidID := str(unpaid, "invoice", "id")
	assert.Equal(t, "open", str(unpaid, "invoice", "status"))
	b.B.fail(t, http.MethodPost, "/api/v1/invoices/"+unpaidID+"/void", nil, http.StatusForbidden)
	assert.Equal(t, "void", str(p.json(t, http.MethodPost, "/api/v1/invoices/"+unpaidID+"/void", nil, http.StatusOK), "status"))

	// Credit: kept by Stripe, it pays the next invoice by itself.
	p.do(t, http.MethodPost, b.path("/credits"), obj{"amount": 2500, "description": "e2e outage"}, http.StatusCreated, nil)
	assert.Equal(t, int64(-2500), balance(t, b), "negative = credit for the company")
	next := p.json(t, http.MethodPost, b.path("/charges"),
		obj{"description": "e2e extra cable", "amount": 1000, "charge_now": false}, http.StatusCreated)
	assert.Equal(t, "paid", str(next, "invoice", "status"), "paid from the credit, no card involved")
	assert.Equal(t, int64(0), num(next, "invoice", "amount_due"))
	assert.Equal(t, int64(-1500), balance(t, b), "$25 credit - $10")
}
