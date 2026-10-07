package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeSubscriptionService struct {
	gotCompany     uuid.UUID
	gotCreate      service.CreateSubscriptionInput
	gotChange      service.ChangeSubscriptionInput
	gotAtPeriodEnd *bool
	gotList        service.SubscriptionListParams
	state          *service.SubscriptionState
	preview        *model.ProrationPreview
	err            error
}

func (f *fakeSubscriptionService) PreviewChange(_ context.Context, _ auth.Principal, companyID uuid.UUID, in service.ChangeSubscriptionInput) (*model.ProrationPreview, error) {
	f.gotCompany, f.gotChange = companyID, in
	return f.preview, f.err
}

func (f *fakeSubscriptionService) Change(_ context.Context, _ auth.Principal, companyID uuid.UUID, in service.ChangeSubscriptionInput) (*service.SubscriptionState, error) {
	f.gotCompany, f.gotChange = companyID, in
	return f.state, f.err
}

func (f *fakeSubscriptionService) Cancel(_ context.Context, _ auth.Principal, companyID uuid.UUID, atPeriodEnd bool) (*model.Subscription, error) {
	f.gotCompany, f.gotAtPeriodEnd = companyID, &atPeriodEnd
	if f.err != nil {
		return nil, f.err
	}
	return f.state.Subscription, nil
}

func (f *fakeSubscriptionService) ScheduleChange(_ context.Context, _ auth.Principal, companyID uuid.UUID, in service.ChangeSubscriptionInput) (*model.Subscription, error) {
	f.gotCompany, f.gotChange = companyID, in
	if f.err != nil {
		return nil, f.err
	}
	return f.state.Subscription, nil
}

func (f *fakeSubscriptionService) CancelScheduledChange(_ context.Context, _ auth.Principal, companyID uuid.UUID) (*model.Subscription, error) {
	f.gotCompany = companyID
	if f.err != nil {
		return nil, f.err
	}
	return f.state.Subscription, nil
}

func (f *fakeSubscriptionService) Resume(_ context.Context, _ auth.Principal, companyID uuid.UUID) (*model.Subscription, error) {
	f.gotCompany = companyID
	if f.err != nil {
		return nil, f.err
	}
	return f.state.Subscription, nil
}

func (f *fakeSubscriptionService) Create(_ context.Context, _ auth.Principal, companyID uuid.UUID, in service.CreateSubscriptionInput) (*service.SubscriptionState, error) {
	f.gotCompany, f.gotCreate = companyID, in
	return f.state, f.err
}

func (f *fakeSubscriptionService) Current(_ context.Context, _ auth.Principal, companyID uuid.UUID) (*model.Subscription, error) {
	f.gotCompany = companyID
	if f.err != nil {
		return nil, f.err
	}
	return f.state.Subscription, nil
}

func (f *fakeSubscriptionService) Refresh(_ context.Context, _ auth.Principal, companyID uuid.UUID) (*service.SubscriptionState, error) {
	f.gotCompany = companyID
	return f.state, f.err
}

func (f *fakeSubscriptionService) List(_ context.Context, _ auth.Principal, params service.SubscriptionListParams) (*service.SubscriptionPage, error) {
	f.gotList = params
	return &service.SubscriptionPage{Items: []model.Subscription{*f.state.Subscription}, Total: 1, Page: 1, PageSize: 20}, f.err
}

func sampleSubscriptionState(withPayment bool) *service.SubscriptionState {
	sub := &model.Subscription{ID: uuid.New(), CompanyID: testCompanyID, StripeSubscriptionID: "sub_1",
		Status: model.SubscriptionIncomplete, Interval: model.IntervalMonth,
		Items: []model.SubscriptionItem{
			{ID: uuid.New(), ProductName: "ELD Fleet plan", Quantity: 12,
				Price: model.Price{ID: testPriceID, UnitAmount: 2500, BillingUnit: model.BillingUnitTruck, Nickname: "Monthly"}},
			{ID: uuid.New(), ProductName: "Support", Quantity: 1,
				Price: model.Price{ID: uuid.New(), UnitAmount: 10000, BillingUnit: model.BillingUnitCompany}},
		}}
	st := &service.SubscriptionState{Subscription: sub}
	if withPayment {
		st.Payment = &service.PaymentDue{InvoiceID: "in_1", AmountDue: 40000, ClientSecret: "pi_secret", PaymentMethodID: "pm_1"}
	}
	return st
}

func subscriptionRouter(svc SubscriptionService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewSubscriptionHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/companies/:id/billing/subscription")
	g.POST("", h.Create)
	g.GET("", h.Current)
	g.POST("/refresh", h.Refresh)
	g.POST("/preview", h.Preview)
	g.PUT("", h.Change)
	g.POST("/cancel", h.Cancel)
	g.POST("/resume", h.Resume)
	g.PUT("/schedule", h.ScheduleChange)
	g.DELETE("/schedule", h.CancelScheduledChange)
	r.GET("/api/v1/subscriptions", h.List)
	return r
}

func TestSubscriptionHandler_Create(t *testing.T) {
	svc := &fakeSubscriptionService{state: sampleSubscriptionState(true)}

	rec := do(subscriptionRouter(svc), http.MethodPost, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription",
		`{"price_ids":["`+testPriceID.String()+`"]}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, testCompanyID, svc.gotCompany)
	assert.Equal(t, []uuid.UUID{testPriceID}, svc.gotCreate.PriceIDs)

	var got SubscriptionStateResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "incomplete", got.Subscription.Status)
	require.Len(t, got.Subscription.Items, 2)
	assert.Equal(t, int64(30000), got.Subscription.Items[0].Amount, "12 trucks × $25")
	assert.Equal(t, int64(40000), got.Subscription.TotalAmount)
	require.NotNil(t, got.Payment)
	assert.Equal(t, "pi_secret", got.Payment.ClientSecret)
	assert.Equal(t, "pm_1", got.Payment.PaymentMethodID)
}

func TestSubscriptionHandler_Create_BadPriceID(t *testing.T) {
	svc := &fakeSubscriptionService{state: sampleSubscriptionState(true)}

	rec := do(subscriptionRouter(svc), http.MethodPost, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription",
		`{"price_ids":["not-a-uuid"]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSubscriptionHandler_Create_AlreadySubscribedIs409(t *testing.T) {
	svc := &fakeSubscriptionService{err: model.ErrSubscriptionExists}

	rec := do(subscriptionRouter(svc), http.MethodPost, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription",
		`{"price_ids":["`+testPriceID.String()+`"]}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "conflict", decodeError(t, rec).Code)
}

func TestSubscriptionHandler_CurrentAndRefresh(t *testing.T) {
	svc := &fakeSubscriptionService{state: sampleSubscriptionState(false)}
	r := subscriptionRouter(svc)
	base := "/api/v1/companies/" + testCompanyID.String() + "/billing/subscription"

	rec := do(r, http.MethodGet, base, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"stripe_subscription_id":"sub_1"`)

	rec = do(r, http.MethodPost, base+"/refresh", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"payment":null`, "nothing to pay is an explicit null")

	svc.err = model.ErrNotFound
	assert.Equal(t, http.StatusNotFound, do(r, http.MethodGet, base, "").Code, "no subscription yet")
}

func TestSubscriptionHandler_List(t *testing.T) {
	svc := &fakeSubscriptionService{state: sampleSubscriptionState(false)}
	r := subscriptionRouter(svc)

	rec := do(r, http.MethodGet, "/api/v1/subscriptions?status=past_due&company_id="+testCompanyID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "past_due", svc.gotList.Status)
	assert.Equal(t, &testCompanyID, svc.gotList.CompanyID)

	assert.Equal(t, http.StatusBadRequest, do(r, http.MethodGet, "/api/v1/subscriptions?status=happy", "").Code)
}

func TestSubscriptionHandler_Preview(t *testing.T) {
	svc := &fakeSubscriptionService{preview: &model.ProrationPreview{ProrationDate: 1791331200, Total: -2500, AmountDue: 0,
		Lines: []model.PreviewLine{{Description: "Unused time on Support", Amount: -2500, Proration: true}}}}

	rec := do(subscriptionRouter(svc), http.MethodPost, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription/preview",
		`{"price_ids":["`+testPriceID.String()+`"]}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"proration_date":1791331200,"amount_due":0,"credit":2500,
		"lines":[{"description":"Unused time on Support","amount":-2500,"proration":true}]}`, rec.Body.String(),
		"a negative total is shown as credit")
}

func TestSubscriptionHandler_Change(t *testing.T) {
	svc := &fakeSubscriptionService{state: sampleSubscriptionState(false)}

	rec := do(subscriptionRouter(svc), http.MethodPut, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription",
		`{"price_ids":["`+testPriceID.String()+`"],"proration_date":1791331200}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, svc.gotChange.ProrationDate)
	assert.Equal(t, int64(1791331200), *svc.gotChange.ProrationDate)
}

func TestSubscriptionHandler_Change_CardDeclined(t *testing.T) {
	svc := &fakeSubscriptionService{err: fmt.Errorf("%w: %s", model.ErrCardDeclined, "Your card has insufficient funds.")}

	rec := do(subscriptionRouter(svc), http.MethodPut, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription",
		`{"price_ids":["`+testPriceID.String()+`"]}`)

	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	body := decodeError(t, rec)
	assert.Equal(t, "card_declined", body.Code)
	assert.Equal(t, "Your card has insufficient funds.", body.Message, "the bank's words, for the person paying")
}

func TestSubscriptionHandler_CancelAndResume(t *testing.T) {
	svc := &fakeSubscriptionService{state: sampleSubscriptionState(false)}
	r := subscriptionRouter(svc)
	base := "/api/v1/companies/" + testCompanyID.String() + "/billing/subscription"

	require.Equal(t, http.StatusOK, do(r, http.MethodPost, base+"/cancel", "").Code)
	assert.True(t, *svc.gotAtPeriodEnd, "no body: at the end of the period (the safe default)")

	require.Equal(t, http.StatusOK, do(r, http.MethodPost, base+"/cancel", `{"at_period_end":false}`).Code)
	assert.False(t, *svc.gotAtPeriodEnd)

	assert.Equal(t, http.StatusOK, do(r, http.MethodPost, base+"/resume", "").Code)

	svc.err = model.ErrForbidden
	assert.Equal(t, http.StatusForbidden, do(r, http.MethodPost, base+"/cancel", `{"at_period_end":false}`).Code)
}

func TestSubscriptionHandler_ScheduledChange(t *testing.T) {
	st := sampleSubscriptionState(false)
	at := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	st.Subscription.ScheduledChangeAt = &at
	st.Subscription.ScheduledItems = []model.SubscriptionItem{{ProductName: "ELD Fleet plan", Quantity: 12,
		Price: model.Price{ID: testPriceID, UnitAmount: 25000, Interval: model.IntervalYear, BillingUnit: model.BillingUnitTruck}}}
	svc := &fakeSubscriptionService{state: st}
	r := subscriptionRouter(svc)
	base := "/api/v1/companies/" + testCompanyID.String() + "/billing/subscription/schedule"

	rec := do(r, http.MethodPut, base, `{"price_ids":["`+testPriceID.String()+`"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []uuid.UUID{testPriceID}, svc.gotChange.PriceIDs)
	var got SubscriptionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.ScheduledChange)
	assert.Equal(t, "year", got.ScheduledChange.Interval)
	assert.Equal(t, int64(300000), got.ScheduledChange.TotalAmount, "12 trucks x $250")
	assert.Equal(t, int64(40000), got.TotalAmount, "today's plan is unchanged")

	assert.Equal(t, http.StatusOK, do(r, http.MethodDelete, base, "").Code)

	st.Subscription.ScheduledChangeAt, st.Subscription.ScheduledItems = nil, nil
	rec = do(r, http.MethodGet, "/api/v1/companies/"+testCompanyID.String()+"/billing/subscription", "")
	assert.Contains(t, rec.Body.String(), `"scheduled_change":null`)
}
