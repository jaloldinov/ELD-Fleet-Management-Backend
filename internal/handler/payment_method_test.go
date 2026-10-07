package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeCustomerService struct {
	gotCompany uuid.UUID
	gotPM      string
	cards      []model.PaymentMethod
	err        error
}

func (f *fakeCustomerService) PublishableKey() string { return "pk_test_1" }

func (f *fakeCustomerService) CreateSetupIntent(_ context.Context, _ auth.Principal, companyID uuid.UUID) (*service.SetupIntent, error) {
	f.gotCompany = companyID
	if f.err != nil {
		return nil, f.err
	}
	return &service.SetupIntent{ClientSecret: "seti_secret", PublishableKey: "pk_test_1"}, nil
}

func (f *fakeCustomerService) ListPaymentMethods(_ context.Context, _ auth.Principal, companyID uuid.UUID) ([]model.PaymentMethod, error) {
	f.gotCompany = companyID
	return f.cards, f.err
}

func (f *fakeCustomerService) SetDefaultPaymentMethod(_ context.Context, _ auth.Principal, companyID uuid.UUID, pm string) error {
	f.gotCompany, f.gotPM = companyID, pm
	return f.err
}

func (f *fakeCustomerService) RemovePaymentMethod(_ context.Context, _ auth.Principal, companyID uuid.UUID, pm string) error {
	f.gotCompany, f.gotPM = companyID, pm
	return f.err
}

func paymentMethodRouter(svc CustomerService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewPaymentMethodHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/companies/:id/billing")
	g.POST("/setup-intent", h.CreateSetupIntent)
	g.GET("/payment-methods", h.List)
	g.PUT("/payment-methods/:pm/default", h.SetDefault)
	g.DELETE("/payment-methods/:pm", h.Remove)
	return r
}

func TestPaymentMethodHandler_SetupIntent(t *testing.T) {
	svc := &fakeCustomerService{}

	rec := do(paymentMethodRouter(svc), http.MethodPost, "/api/v1/companies/"+testCompanyID.String()+"/billing/setup-intent", "")

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, testCompanyID, svc.gotCompany)
	var got SetupIntentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, SetupIntentResponse{ClientSecret: "seti_secret", PublishableKey: "pk_test_1"}, got)
}

func TestPaymentMethodHandler_List(t *testing.T) {
	svc := &fakeCustomerService{cards: []model.PaymentMethod{
		{ID: "pm_a", CustomerID: "cus_1", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030, IsDefault: true},
	}}

	rec := do(paymentMethodRouter(svc), http.MethodGet, "/api/v1/companies/"+testCompanyID.String()+"/billing/payment-methods", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"items":[{"id":"pm_a","brand":"visa","last4":"4242","exp_month":12,"exp_year":2030,"is_default":true}]}`,
		rec.Body.String(), "no customer id or other Stripe internals")

	svc.cards = nil
	rec = do(paymentMethodRouter(svc), http.MethodGet, "/api/v1/companies/"+testCompanyID.String()+"/billing/payment-methods", "")
	assert.JSONEq(t, `{"items":[]}`, rec.Body.String())
}

func TestPaymentMethodHandler_SetDefaultAndRemove(t *testing.T) {
	svc := &fakeCustomerService{}
	r := paymentMethodRouter(svc)
	base := "/api/v1/companies/" + testCompanyID.String() + "/billing/payment-methods/pm_a"

	assert.Equal(t, http.StatusNoContent, do(r, http.MethodPut, base+"/default", "").Code)
	assert.Equal(t, "pm_a", svc.gotPM)

	assert.Equal(t, http.StatusNoContent, do(r, http.MethodDelete, base, "").Code)
	assert.Equal(t, testCompanyID, svc.gotCompany)

	svc.err = model.ErrNotFound
	assert.Equal(t, http.StatusNotFound, do(r, http.MethodDelete, base, "").Code, "another company's card")
}

func TestPaymentMethodHandler_Config(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/billing/config", NewPaymentMethodHandler(&fakeCustomerService{}).Config)

	rec := do(r, http.MethodGet, "/api/v1/billing/config", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"publishable_key":"pk_test_1"}`, rec.Body.String())
}
