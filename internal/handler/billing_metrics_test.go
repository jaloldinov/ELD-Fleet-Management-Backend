package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeMetricsService struct{ gotMonths int }

func (f *fakeMetricsService) Get(_ context.Context, _ auth.Principal, months int) (*model.BillingMetrics, error) {
	f.gotMonths = months
	return &model.BillingMetrics{MRR: 25000, MRRByProduct: []model.ProductMRR{{ProductName: "ELD", MRR: 25000, Subscriptions: 2}},
		SubscriptionsByStatus: map[string]int{"active": 2}, CompaniesByBillingStatus: map[string]int{"active": 2},
		OutstandingInvoices: 1, OutstandingAmount: 4200, PaymentFailures30d: 3,
		Months: []model.MonthMetrics{{Month: "2026-10", Revenue: 15000}}}, nil
}

func TestBillingMetricsHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &fakeMetricsService{}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	r.GET("/api/v1/billing/metrics", NewBillingMetricsHandler(svc).Get)

	rec := do(r, http.MethodGet, "/api/v1/billing/metrics?months=6", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 6, svc.gotMonths)
	var got BillingMetricsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, int64(25000), got.MRR)
	assert.Equal(t, int64(300000), got.ARR, "MRR × 12")
	assert.Equal(t, OutstandingResponse{Invoices: 1, Amount: 4200}, got.Outstanding)
	require.Len(t, got.Months, 1)

	assert.Equal(t, http.StatusBadRequest, do(r, http.MethodGet, "/api/v1/billing/metrics?months=many", "").Code)
}
