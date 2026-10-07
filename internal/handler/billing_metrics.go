package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// BillingMetricsService is what BillingMetricsHandler needs.
type BillingMetricsService interface {
	Get(ctx context.Context, p auth.Principal, months int) (*model.BillingMetrics, error)
}

type ProductMRRResponse struct {
	ProductID     uuid.UUID `json:"product_id" swaggertype:"string" format:"uuid"`
	ProductName   string    `json:"product_name" example:"ELD Fleet plan"`
	MRR           int64     `json:"mrr" example:"150000"`
	Subscriptions int       `json:"subscriptions" example:"12"`
}

type MonthMetricsResponse struct {
	Month string `json:"month" example:"2026-10"`
	// Cents collected that month (paid invoices, by payment date; refunds not subtracted).
	Revenue               int64 `json:"revenue" example:"240000"`
	NewSubscriptions      int   `json:"new_subscriptions" example:"3"`
	CanceledSubscriptions int   `json:"canceled_subscriptions" example:"1"`
}

type OutstandingResponse struct {
	Invoices int   `json:"invoices" example:"2"`
	Amount   int64 `json:"amount" example:"30000"`
}

type BillingMetricsResponse struct {
	Currency string `json:"currency" example:"usd"`
	// Monthly recurring revenue in cents: active and past_due subscriptions,
	// yearly prices counted as 1/12. Discounts and credit are not subtracted.
	MRR          int64                `json:"mrr" example:"250000"`
	ARR          int64                `json:"arr" example:"3000000"`
	MRRByProduct []ProductMRRResponse `json:"mrr_by_product"`
	// All subscriptions (ended ones too) by status.
	SubscriptionsByStatus map[string]int `json:"subscriptions_by_status"`
	// Companies by billing status: none, pending, active, past_due, blocked.
	CompaniesByBillingStatus map[string]int      `json:"companies_by_billing_status"`
	Outstanding              OutstandingResponse `json:"outstanding"`
	// Failed payment attempts Stripe reported in the last 30 days (retries count).
	PaymentFailures30d int `json:"payment_failures_30d" example:"3"`
	// Oldest first; every month present.
	Months []MonthMetricsResponse `json:"months"`
}

type BillingMetricsHandler struct {
	svc BillingMetricsService
}

func NewBillingMetricsHandler(svc BillingMetricsService) *BillingMetricsHandler {
	return &BillingMetricsHandler{svc: svc}
}

// Get handles GET /api/v1/billing/metrics.
//
//	@Summary		Billing dashboard
//	@Description	Requires permission: billing_metrics:read (platform only). MRR, subscriptions and companies by status, outstanding invoices, payment failures and monthly revenue.
//	@Description	Computed from our copies (no Stripe calls).
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			months	query		int	false	"How many months of history, the current one included"	default(12)	minimum(1)	maximum(36)
//	@Success		200		{object}	BillingMetricsResponse
//	@Failure		400		{object}	apierror.Response
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission billing_metrics:read, or not a platform user"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/billing/metrics [get]
func (h *BillingMetricsHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	months, ok := queryInt(c, "months")
	if !ok {
		handleError(c, &validation.ValidationError{Fields: map[string]string{"months": "must be an integer"}})
		return
	}
	m, err := h.svc.Get(c.Request.Context(), p, months)
	if err != nil {
		handleError(c, err)
		return
	}

	out := BillingMetricsResponse{
		Currency: model.CurrencyUSD, MRR: m.MRR, ARR: m.MRR * 12,
		MRRByProduct:             make([]ProductMRRResponse, 0, len(m.MRRByProduct)),
		SubscriptionsByStatus:    m.SubscriptionsByStatus,
		CompaniesByBillingStatus: m.CompaniesByBillingStatus,
		Outstanding:              OutstandingResponse{Invoices: m.OutstandingInvoices, Amount: m.OutstandingAmount},
		PaymentFailures30d:       m.PaymentFailures30d,
		Months:                   make([]MonthMetricsResponse, 0, len(m.Months)),
	}
	for _, p := range m.MRRByProduct {
		out.MRRByProduct = append(out.MRRByProduct, ProductMRRResponse{ProductID: p.ProductID, ProductName: p.ProductName,
			MRR: p.MRR, Subscriptions: p.Subscriptions})
	}
	for _, mm := range m.Months {
		out.Months = append(out.Months, MonthMetricsResponse{Month: mm.Month, Revenue: mm.Revenue,
			NewSubscriptions: mm.NewSubscriptions, CanceledSubscriptions: mm.CanceledSubscriptions})
	}
	c.JSON(http.StatusOK, out)
}
