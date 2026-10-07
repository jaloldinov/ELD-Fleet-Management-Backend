package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// SubscriptionService is what SubscriptionHandler needs from the service layer.
type SubscriptionService interface {
	Create(ctx context.Context, p auth.Principal, companyID uuid.UUID, in service.CreateSubscriptionInput) (*service.SubscriptionState, error)
	Current(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.Subscription, error)
	Refresh(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*service.SubscriptionState, error)
	List(ctx context.Context, p auth.Principal, params service.SubscriptionListParams) (*service.SubscriptionPage, error)
}

// ---------- DTOs ----------

type CreateSubscriptionRequest struct {
	// One price per product, all with the same interval (all monthly or all yearly).
	PriceIDs []uuid.UUID `json:"price_ids" swaggertype:"array,string" example:"0199a1b2-0000-7000-8000-0000000000b1"`
}

type SubscriptionItemResponse struct {
	ID          uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	PriceID     uuid.UUID `json:"price_id" swaggertype:"string" format:"uuid"`
	ProductName string    `json:"product_name" example:"ELD Fleet plan"`
	Nickname    string    `json:"nickname" example:"Monthly"`
	UnitAmount  int64     `json:"unit_amount" example:"2500"`
	BillingUnit string    `json:"billing_unit" example:"truck"`
	Quantity    int       `json:"quantity" example:"12"`
	// unit_amount × quantity, in cents.
	Amount int64 `json:"amount" example:"30000"`
}

type SubscriptionResponse struct {
	ID        uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	CompanyID uuid.UUID `json:"company_id" swaggertype:"string" format:"uuid"`
	// incomplete (not paid yet), active, past_due, unpaid, canceled, incomplete_expired, trialing, paused.
	Status             string                     `json:"status" example:"active"`
	Interval           string                     `json:"interval" example:"month"`
	CurrentPeriodStart *time.Time                 `json:"current_period_start" example:"2026-10-07T00:00:00Z"`
	CurrentPeriodEnd   *time.Time                 `json:"current_period_end" example:"2026-11-07T00:00:00Z"`
	CancelAtPeriodEnd  bool                       `json:"cancel_at_period_end" example:"false"`
	CanceledAt         *time.Time                 `json:"canceled_at"`
	Items              []SubscriptionItemResponse `json:"items"`
	// Sum of the items' amounts: what one interval costs, in cents (before
	// proration, credits and tax).
	TotalAmount          int64     `json:"total_amount" example:"40000"`
	StripeSubscriptionID string    `json:"stripe_subscription_id" example:"sub_1QabcDEFghiJKL"`
	CreatedAt            time.Time `json:"created_at" example:"2026-10-07T11:26:26Z"`
}

type PaymentDueResponse struct {
	InvoiceID string `json:"invoice_id" example:"in_1QabcDEF"`
	AmountDue int64  `json:"amount_due" example:"40000"`
	// For stripe.confirmPayment({clientSecret, confirmParams: {payment_method}}).
	ClientSecret    string `json:"client_secret" example:"pi_1Qabc_secret_xyz"`
	PaymentMethodID string `json:"payment_method_id" example:"pm_1QabcDEF"`
}

type SubscriptionStateResponse struct {
	Subscription SubscriptionResponse `json:"subscription"`
	// Present while the first invoice is unpaid: pay it in the browser,
	// then POST .../subscription/refresh. null = nothing to pay now.
	Payment *PaymentDueResponse `json:"payment"`
}

type ListSubscriptionsResponse struct {
	Items    []SubscriptionResponse `json:"items"`
	Page     int                    `json:"page" example:"1"`
	PageSize int                    `json:"page_size" example:"20"`
	Total    int                    `json:"total" example:"4"`
}

func newSubscriptionResponse(s *model.Subscription) SubscriptionResponse {
	items := make([]SubscriptionItemResponse, 0, len(s.Items))
	var total int64
	for _, it := range s.Items {
		amount := it.Price.UnitAmount * int64(it.Quantity)
		total += amount
		items = append(items, SubscriptionItemResponse{
			ID: it.ID, PriceID: it.Price.ID, ProductName: it.ProductName, Nickname: it.Price.Nickname,
			UnitAmount: it.Price.UnitAmount, BillingUnit: it.Price.BillingUnit, Quantity: it.Quantity, Amount: amount,
		})
	}
	return SubscriptionResponse{
		ID: s.ID, CompanyID: s.CompanyID, Status: s.Status, Interval: s.Interval,
		CurrentPeriodStart: utc(s.CurrentPeriodStart), CurrentPeriodEnd: utc(s.CurrentPeriodEnd),
		CancelAtPeriodEnd: s.CancelAtPeriodEnd, CanceledAt: utc(s.CanceledAt),
		Items: items, TotalAmount: total, StripeSubscriptionID: s.StripeSubscriptionID, CreatedAt: s.CreatedAt.UTC(),
	}
}

// utc keeps every time in the API in UTC, like CreatedAt elsewhere (the
// database driver returns the server's local zone).
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func newSubscriptionStateResponse(st *service.SubscriptionState) SubscriptionStateResponse {
	out := SubscriptionStateResponse{Subscription: newSubscriptionResponse(st.Subscription)}
	if pd := st.Payment; pd != nil {
		out.Payment = &PaymentDueResponse{
			InvoiceID: pd.InvoiceID, AmountDue: pd.AmountDue, ClientSecret: pd.ClientSecret, PaymentMethodID: pd.PaymentMethodID,
		}
	}
	return out
}

// ---------- Handler ----------

type SubscriptionHandler struct {
	svc SubscriptionService
}

func NewSubscriptionHandler(svc SubscriptionService) *SubscriptionHandler {
	return &SubscriptionHandler{svc: svc}
}

// Create handles POST /api/v1/companies/:id/billing/subscription.
//
//	@Summary		Subscribe (pay first)
//	@Description	Requires permission: subscription:manage. Company users: own company only.
//	@Description	Starts the subscription as "incomplete" with an open first invoice; nothing is charged yet.
//	@Description	Pay it in the browser with Stripe.js using payment.client_secret, then call .../subscription/refresh: the status becomes "active".
//	@Description	Per-truck prices are billed for the company's active trucks; flat prices once. Needs a default card.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string						true	"Company ID"	format(uuid)
//	@Param			body	body		CreateSubscriptionRequest	true	"Prices to subscribe to"
//	@Success		201		{object}	SubscriptionStateResponse
//	@Failure		400		{object}	apierror.Response	"validation failed: unknown/archived price, mixed intervals, no default card"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission subscription:manage"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		409		{object}	apierror.Response	"the company already has a subscription"
//	@Failure		500		{object}	apierror.Response
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/subscription [post]
func (h *SubscriptionHandler) Create(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req CreateSubscriptionRequest
	if !bindJSON(c, &req) {
		return
	}
	st, err := h.svc.Create(c.Request.Context(), p, companyID, service.CreateSubscriptionInput{PriceIDs: req.PriceIDs})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newSubscriptionStateResponse(st))
}

// Current handles GET /api/v1/companies/:id/billing/subscription.
//
//	@Summary		The company's current subscription
//	@Description	Requires permission: subscription:read. Company users: own company only.
//	@Description	The subscription that has not ended (any status but canceled and incomplete_expired), as last synced from Stripe.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Company ID"	format(uuid)
//	@Success		200	{object}	SubscriptionResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission subscription:read"
//	@Failure		404	{object}	apierror.Response	"no current subscription (or company not found)"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/companies/{id}/billing/subscription [get]
func (h *SubscriptionHandler) Current(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	sub, err := h.svc.Current(c.Request.Context(), p, companyID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newSubscriptionResponse(sub))
}

// Refresh handles POST /api/v1/companies/:id/billing/subscription/refresh.
//
//	@Summary		Re-read the subscription from Stripe
//	@Description	Requires permission: subscription:read. Company users: own company only.
//	@Description	Call after paying in the browser. While still unpaid, payment says how to pay (again).
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Company ID"	format(uuid)
//	@Success		200	{object}	SubscriptionStateResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission subscription:read"
//	@Failure		404	{object}	apierror.Response	"no current subscription (or company not found)"
//	@Failure		500	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/subscription/refresh [post]
func (h *SubscriptionHandler) Refresh(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	st, err := h.svc.Refresh(c.Request.Context(), p, companyID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newSubscriptionStateResponse(st))
}

// List handles GET /api/v1/subscriptions.
//
//	@Summary		List subscriptions
//	@Description	Requires permission: subscription:read. Newest first, ended ones included (history).
//	@Description	Company users get only their own company's (company_id is ignored).
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			company_id	query		string	false	"Filter by company (platform users only)"	format(uuid)
//	@Param			status		query		string	false	"Filter by status"							Enums(incomplete, incomplete_expired, trialing, active, past_due, unpaid, canceled, paused)
//	@Param			page		query		int		false	"Page number (1-based)"						default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"							default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListSubscriptionsResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission subscription:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/subscriptions [get]
func (h *SubscriptionHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var v validation.Validator
	page, ok := queryInt(c, "page")
	v.Check(ok, "page", "must be an integer")
	size, ok := queryInt(c, "page_size")
	v.Check(ok, "page_size", "must be an integer")
	var companyID *uuid.UUID
	if s := c.Query("company_id"); s != "" {
		id, err := uuid.Parse(s)
		v.Check(err == nil, "company_id", "must be a valid UUID")
		companyID = &id
	}
	status := c.Query("status")
	v.Check(status == "" || validSubscriptionStatus[status], "status", "unknown status")
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	res, err := h.svc.List(c.Request.Context(), p, service.SubscriptionListParams{
		CompanyID: companyID, Status: status, Page: page, PageSize: size,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]SubscriptionResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newSubscriptionResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListSubscriptionsResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

var validSubscriptionStatus = map[string]bool{
	model.SubscriptionIncomplete: true, model.SubscriptionIncompleteExpired: true, model.SubscriptionTrialing: true,
	model.SubscriptionActive: true, model.SubscriptionPastDue: true, model.SubscriptionUnpaid: true,
	model.SubscriptionCanceled: true, model.SubscriptionPaused: true,
}
