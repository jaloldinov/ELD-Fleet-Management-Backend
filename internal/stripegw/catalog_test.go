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

// recorded is one request the fake Stripe received.
type recorded struct {
	Method, Path   string
	Form           url.Values
	IdempotencyKey string
}

// fakeStripe answers every request with status and body and records what
// it received. Stripe's API takes form-encoded bodies, not JSON.
type fakeStripe struct {
	mu   sync.Mutex
	reqs []recorded
}

func newFake(t *testing.T, status int, body string) (*fakeStripe, *stripe.Client) {
	t.Helper()
	f := &fakeStripe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, form, r.Header.Get("Idempotency-Key")})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := stripeclient.New(stripeclient.Config{SecretKey: "sk_test_fake", URL: srv.URL, Log: slog.New(slog.DiscardHandler)})
	return f, c
}

func (f *fakeStripe) only(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.reqs, 1)
	return f.reqs[0]
}

func TestCatalog_CreateProduct(t *testing.T) {
	f, c := newFake(t, http.StatusOK, `{"id":"prod_123","object":"product"}`)
	p := model.Product{ID: uuid.Must(uuid.NewV7()), Name: "ELD Fleet plan", Description: "HOS for every truck"}

	id, err := NewCatalog(c).CreateProduct(context.Background(), p)

	require.NoError(t, err)
	assert.Equal(t, "prod_123", id)
	r := f.only(t)
	assert.Equal(t, "POST /v1/products", r.Method+" "+r.Path)
	assert.Equal(t, "ELD Fleet plan", r.Form.Get("name"))
	assert.Equal(t, "HOS for every truck", r.Form.Get("description"))
	assert.Equal(t, p.ID.String(), r.Form.Get("metadata[eld_product_id]"), "our id travels to Stripe")
	assert.Equal(t, "product-create-"+p.ID.String(), r.IdempotencyKey)
}

func TestCatalog_CreateProduct_NoEmptyDescription(t *testing.T) {
	f, c := newFake(t, http.StatusOK, `{"id":"prod_123","object":"product"}`)

	_, err := NewCatalog(c).CreateProduct(context.Background(), model.Product{ID: uuid.New(), Name: "Plan"})

	require.NoError(t, err)
	_, sent := f.only(t).Form["description"]
	assert.False(t, sent, "Stripe refuses an empty description")
}

func TestCatalog_UpdateProduct(t *testing.T) {
	f, c := newFake(t, http.StatusOK, `{"id":"prod_123","object":"product"}`)

	err := NewCatalog(c).UpdateProduct(context.Background(), model.Product{
		StripeProductID: "prod_123", Name: "Renamed", IsActive: false,
	})

	require.NoError(t, err)
	r := f.only(t)
	assert.Equal(t, "POST /v1/products/prod_123", r.Method+" "+r.Path)
	assert.Equal(t, "Renamed", r.Form.Get("name"))
	assert.Equal(t, "false", r.Form.Get("active"))
	assert.Equal(t, "", r.Form.Get("description"), "an empty description is unset")
	_, sent := r.Form["description"]
	assert.True(t, sent)
}

func TestCatalog_CreatePrice(t *testing.T) {
	f, c := newFake(t, http.StatusOK, `{"id":"price_123","object":"price"}`)
	pr := model.Price{
		ID: uuid.Must(uuid.NewV7()), Nickname: "Monthly", Currency: model.CurrencyUSD,
		UnitAmount: 2500, Interval: model.IntervalMonth, BillingUnit: model.BillingUnitTruck,
	}

	id, err := NewCatalog(c).CreatePrice(context.Background(), "prod_123", pr)

	require.NoError(t, err)
	assert.Equal(t, "price_123", id)
	r := f.only(t)
	assert.Equal(t, "POST /v1/prices", r.Method+" "+r.Path)
	assert.Equal(t, "prod_123", r.Form.Get("product"))
	assert.Equal(t, "usd", r.Form.Get("currency"))
	assert.Equal(t, "2500", r.Form.Get("unit_amount"), "cents, as an integer")
	assert.Equal(t, "month", r.Form.Get("recurring[interval]"))
	assert.Equal(t, "Monthly", r.Form.Get("nickname"))
	assert.Equal(t, pr.ID.String(), r.Form.Get("metadata[eld_price_id]"))
	assert.Equal(t, "truck", r.Form.Get("metadata[eld_billing_unit]"))
	assert.Equal(t, "price-create-"+pr.ID.String(), r.IdempotencyKey)
}

func TestCatalog_UpdatePrice(t *testing.T) {
	f, c := newFake(t, http.StatusOK, `{"id":"price_123","object":"price"}`)

	err := NewCatalog(c).UpdatePrice(context.Background(), model.Price{StripePriceID: "price_123", IsActive: false})

	require.NoError(t, err)
	r := f.only(t)
	assert.Equal(t, "POST /v1/prices/price_123", r.Method+" "+r.Path)
	assert.Equal(t, "false", r.Form.Get("active"))
	for _, field := range []string{"unit_amount", "currency", "recurring[interval]"} {
		_, sent := r.Form[field]
		assert.False(t, sent, "%s never changes", field)
	}
}

func TestCatalog_StripeErrorIsPaymentProviderError(t *testing.T) {
	_, c := newFake(t, http.StatusBadRequest,
		`{"error":{"type":"invalid_request_error","message":"No such product: 'prod_gone'"}}`)

	err := NewCatalog(c).UpdateProduct(context.Background(), model.Product{StripeProductID: "prod_gone", Name: "x"})

	require.ErrorIs(t, err, model.ErrPaymentProvider)
	var sErr *stripe.Error
	require.ErrorAs(t, err, &sErr, "the Stripe error stays inside, for the logs")
	assert.Contains(t, sErr.Msg, "No such product")
}
