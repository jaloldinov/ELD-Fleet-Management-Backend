package stripegw

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/stripeclient"
)

type reply struct {
	status int
	body   string
}

// routedFake answers by "METHOD /path"; unknown routes get a 404 like
// Stripe's. It records every request.
type routedFake struct {
	mu   sync.Mutex
	reqs []recorded
}

func newRoutedFake(t *testing.T, routes map[string]reply) (*routedFake, *stripe.Client) {
	t.Helper()
	f := &routedFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		if r.Method == http.MethodGet || r.Method == http.MethodDelete {
			form = r.URL.Query() // these send their parameters in the URL
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, form, r.Header.Get("Idempotency-Key")})
		f.mu.Unlock()

		rep, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			rep = reply{http.StatusNotFound,
				`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such object"}}`}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	c := stripeclient.New(stripeclient.Config{SecretKey: "sk_test_fake", URL: srv.URL, Log: slog.New(slog.DiscardHandler)})
	return f, c
}

func (f *routedFake) find(t *testing.T, methodPath string) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.Method+" "+r.Path == methodPath {
			return r
		}
	}
	require.FailNow(t, "request not sent", methodPath)
	return recorded{}
}

func TestCustomers_CreateCustomer(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/customers": {200, `{"id":"cus_123","object":"customer"}`},
	})
	co := model.Company{ID: uuid.New(), Name: "Acme Freight LLC", DOTNumber: "1234567"}

	id, err := NewCustomers(c).CreateCustomer(context.Background(), co)

	require.NoError(t, err)
	assert.Equal(t, "cus_123", id)
	r := f.find(t, "POST /v1/customers")
	assert.Equal(t, "Acme Freight LLC", r.Form.Get("name"))
	assert.Equal(t, co.ID.String(), r.Form.Get("metadata[eld_company_id]"))
	assert.Equal(t, "customer-create-"+co.ID.String(), r.IdempotencyKey,
		"racing requests for the same company get the same customer")
}

func TestCustomers_CreateSetupIntent(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/setup_intents": {200, `{"id":"seti_1","object":"setup_intent","client_secret":"seti_1_secret_abc"}`},
	})

	secret, err := NewCustomers(c).CreateSetupIntent(context.Background(), "cus_123")

	require.NoError(t, err)
	assert.Equal(t, "seti_1_secret_abc", secret)
	r := f.find(t, "POST /v1/setup_intents")
	assert.Equal(t, "cus_123", r.Form.Get("customer"))
	assert.Equal(t, "off_session", r.Form.Get("usage"), "cards are charged later, without the user")
	assert.Equal(t, "card", r.Form.Get("allowed_payment_method_types[0]"))
}

func TestCustomers_ListPaymentMethods(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"GET /v1/customers/cus_123": {200, `{"id":"cus_123","object":"customer",
			"invoice_settings":{"default_payment_method":"pm_b"}}`},
		"GET /v1/payment_methods": {200, `{"object":"list","has_more":false,"data":[
			{"id":"pm_a","object":"payment_method","customer":"cus_123","type":"card",
			 "card":{"brand":"visa","last4":"4242","exp_month":12,"exp_year":2030}},
			{"id":"pm_b","object":"payment_method","customer":"cus_123","type":"card",
			 "card":{"brand":"mastercard","last4":"4444","exp_month":1,"exp_year":2031}}]}`},
	})

	pms, err := NewCustomers(c).ListPaymentMethods(context.Background(), "cus_123")

	require.NoError(t, err)
	require.Len(t, pms, 2)
	assert.Equal(t, model.PaymentMethod{ID: "pm_a", CustomerID: "cus_123", Brand: "visa", Last4: "4242",
		ExpMonth: 12, ExpYear: 2030}, pms[0])
	assert.True(t, pms[1].IsDefault)
	assert.False(t, pms[0].IsDefault)
	r := f.find(t, "GET /v1/payment_methods")
	assert.Equal(t, "cus_123", r.Form.Get("customer"))
	assert.Equal(t, "card", r.Form.Get("type"))
}

func TestCustomers_ListPaymentMethods_NoneIsEmptyNotNil(t *testing.T) {
	_, c := newRoutedFake(t, map[string]reply{
		"GET /v1/customers/cus_123": {200, `{"id":"cus_123","object":"customer","invoice_settings":{}}`},
		"GET /v1/payment_methods":   {200, `{"object":"list","has_more":false,"data":[]}`},
	})

	pms, err := NewCustomers(c).ListPaymentMethods(context.Background(), "cus_123")

	require.NoError(t, err)
	assert.NotNil(t, pms)
	assert.Empty(t, pms)
}

func TestCustomers_GetPaymentMethod(t *testing.T) {
	_, c := newRoutedFake(t, map[string]reply{
		"GET /v1/payment_methods/pm_a": {200, `{"id":"pm_a","object":"payment_method","customer":"cus_9","type":"card",
			"card":{"brand":"visa","last4":"4242","exp_month":12,"exp_year":2030}}`},
	})
	g := NewCustomers(c)

	pm, err := g.GetPaymentMethod(context.Background(), "pm_a")
	require.NoError(t, err)
	assert.Equal(t, "cus_9", pm.CustomerID, "the caller checks the owner")

	_, err = g.GetPaymentMethod(context.Background(), "pm_unknown")
	assert.ErrorIs(t, err, model.ErrNotFound, "Stripe's 404 is our 404, not a 502")
}

func TestCustomers_SetDefaultAndDetach(t *testing.T) {
	f, c := newRoutedFake(t, map[string]reply{
		"POST /v1/customers/cus_123":           {200, `{"id":"cus_123","object":"customer"}`},
		"POST /v1/payment_methods/pm_a/detach": {200, `{"id":"pm_a","object":"payment_method"}`},
	})
	g := NewCustomers(c)

	require.NoError(t, g.SetDefaultPaymentMethod(context.Background(), "cus_123", "pm_a"))
	assert.Equal(t, "pm_a", f.find(t, "POST /v1/customers/cus_123").Form.Get("invoice_settings[default_payment_method]"))

	require.NoError(t, g.DetachPaymentMethod(context.Background(), "pm_a"))
	f.find(t, "POST /v1/payment_methods/pm_a/detach")

	err := g.DetachPaymentMethod(context.Background(), "pm_gone")
	assert.ErrorIs(t, err, model.ErrPaymentProvider)
}
