package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// CustomerService is what PaymentMethodHandler needs from the service layer.
type CustomerService interface {
	PublishableKey() string
	CreateSetupIntent(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*service.SetupIntent, error)
	ListPaymentMethods(ctx context.Context, p auth.Principal, companyID uuid.UUID) ([]model.PaymentMethod, error)
	SetDefaultPaymentMethod(ctx context.Context, p auth.Principal, companyID uuid.UUID, paymentMethodID string) error
	RemovePaymentMethod(ctx context.Context, p auth.Principal, companyID uuid.UUID, paymentMethodID string) error
}

// ---------- DTOs ----------

type SetupIntentResponse struct {
	// Give it to Stripe.js (elements + confirmSetup). Single use; it lets the
	// browser save ONE card for this company, nothing else.
	ClientSecret string `json:"client_secret" example:"seti_1Qabc_secret_xyz"`
	// Public key of our Stripe account, for Stripe("pk_...").
	PublishableKey string `json:"publishable_key" example:"pk_test_51Qabc"`
}

type PaymentMethodResponse struct {
	ID        string `json:"id" example:"pm_1QabcDEFghiJKL"`
	Brand     string `json:"brand" example:"visa"`
	Last4     string `json:"last4" example:"4242"`
	ExpMonth  int    `json:"exp_month" example:"12"`
	ExpYear   int    `json:"exp_year" example:"2030"`
	IsDefault bool   `json:"is_default" example:"true"`
}

type ListPaymentMethodsResponse struct {
	Items []PaymentMethodResponse `json:"items"`
}

type BillingConfigResponse struct {
	// Public key of our Stripe account, for Stripe("pk_...").
	PublishableKey string `json:"publishable_key" example:"pk_test_51Qabc"`
}

// ---------- Handler ----------

type PaymentMethodHandler struct {
	svc CustomerService
}

func NewPaymentMethodHandler(svc CustomerService) *PaymentMethodHandler {
	return &PaymentMethodHandler{svc: svc}
}

// Config handles GET /api/v1/billing/config.
//
//	@Summary		Billing settings for the browser
//	@Description	Login only. The Stripe publishable key the page needs to load Stripe.js (public by design).
//	@Description	The route exists only when billing is enabled.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	BillingConfigResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Router			/api/v1/billing/config [get]
func (h *PaymentMethodHandler) Config(c *gin.Context) {
	c.JSON(http.StatusOK, BillingConfigResponse{PublishableKey: h.svc.PublishableKey()})
}

// CreateSetupIntent handles POST /api/v1/companies/:id/billing/setup-intent.
//
//	@Summary		Start adding a card
//	@Description	Requires permission: payment_method:manage. Company users: own company only.
//	@Description	Returns a one-time client secret for Stripe.js: the browser sends the card straight to Stripe, never to this API.
//	@Description	Creates the company's Stripe customer on first use.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Company ID"	format(uuid)
//	@Success		201	{object}	SetupIntentResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission payment_method:manage"
//	@Failure		404	{object}	apierror.Response	"company not found"
//	@Failure		500	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/setup-intent [post]
func (h *PaymentMethodHandler) CreateSetupIntent(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	si, err := h.svc.CreateSetupIntent(c.Request.Context(), p, companyID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, SetupIntentResponse{ClientSecret: si.ClientSecret, PublishableKey: si.PublishableKey})
}

// List handles GET /api/v1/companies/:id/billing/payment-methods.
//
//	@Summary		List saved cards
//	@Description	Requires permission: payment_method:manage. Company users: own company only.
//	@Description	Only brand, last 4 digits and expiry: card numbers never reach this API.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Company ID"	format(uuid)
//	@Success		200	{object}	ListPaymentMethodsResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission payment_method:manage"
//	@Failure		404	{object}	apierror.Response	"company not found"
//	@Failure		500	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/payment-methods [get]
func (h *PaymentMethodHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	pms, err := h.svc.ListPaymentMethods(c.Request.Context(), p, companyID)
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]PaymentMethodResponse, 0, len(pms))
	for _, pm := range pms {
		items = append(items, PaymentMethodResponse{
			ID: pm.ID, Brand: pm.Brand, Last4: pm.Last4, ExpMonth: pm.ExpMonth, ExpYear: pm.ExpYear, IsDefault: pm.IsDefault,
		})
	}
	c.JSON(http.StatusOK, ListPaymentMethodsResponse{Items: items})
}

// SetDefault handles PUT /api/v1/companies/:id/billing/payment-methods/:pm/default.
//
//	@Summary		Make a card the default
//	@Description	Requires permission: payment_method:manage. The default card is charged for invoices.
//	@Tags			billing
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Company ID"	format(uuid)
//	@Param			pm	path	string	true	"Payment method ID (pm_...)"
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission payment_method:manage"
//	@Failure		404	{object}	apierror.Response	"company or card not found (or the card is another company's)"
//	@Failure		500	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/payment-methods/{pm}/default [put]
func (h *PaymentMethodHandler) SetDefault(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.SetDefaultPaymentMethod(c.Request.Context(), p, companyID, c.Param("pm")); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Remove handles DELETE /api/v1/companies/:id/billing/payment-methods/:pm.
//
//	@Summary		Remove a card
//	@Description	Requires permission: payment_method:manage. The card is detached in Stripe and cannot be used again.
//	@Tags			billing
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Company ID"	format(uuid)
//	@Param			pm	path	string	true	"Payment method ID (pm_...)"
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission payment_method:manage"
//	@Failure		404	{object}	apierror.Response	"company or card not found (or the card is another company's)"
//	@Failure		500	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/payment-methods/{pm} [delete]
func (h *PaymentMethodHandler) Remove(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	companyID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.RemovePaymentMethod(c.Request.Context(), p, companyID, c.Param("pm")); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
