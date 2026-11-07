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

// InvoiceService is what InvoiceHandler needs from the service layer.
type InvoiceService interface {
	List(ctx context.Context, p auth.Principal, params service.InvoiceListParams) (*service.InvoicePage, error)
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error)
	CreditNotes(ctx context.Context, p auth.Principal, id uuid.UUID) ([]model.CreditNote, error)
	Balance(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.CustomerBalance, error)
	Pay(ctx context.Context, p auth.Principal, id uuid.UUID) (*service.InvoicePayment, error)
	Finalize(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error)
	Void(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error)
	CreateCharge(ctx context.Context, p auth.Principal, companyID uuid.UUID, in service.ChargeInput) (*service.ChargeResult, error)
	AddCredit(ctx context.Context, p auth.Principal, companyID uuid.UUID, in service.CreditInput) (*model.BalanceTransaction, error)
	CreateCreditNote(ctx context.Context, p auth.Principal, id uuid.UUID, in service.CreditNoteInput) (*model.CreditNote, error)
}

// ---------- DTOs ----------

type InvoiceLineResponse struct {
	Description string `json:"description" example:"12 × ELD Fleet plan (at $25.00 / month)"`
	Amount      int64  `json:"amount" example:"30000"`
	Quantity    int    `json:"quantity" example:"12"`
	Proration   bool   `json:"proration" example:"false"`
}

type InvoiceResponse struct {
	ID        uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	CompanyID uuid.UUID `json:"company_id" swaggertype:"string" format:"uuid"`
	Number    string    `json:"number" example:"ABCD-0001"`
	// draft, open (waiting for payment), paid, void, uncollectible.
	Status string `json:"status" example:"open"`
	// subscription_create, subscription_cycle (renewal), subscription_update (plan change), manual (one-off charge).
	BillingReason string `json:"billing_reason" example:"subscription_cycle"`
	Description   string `json:"description" example:""`
	Currency      string `json:"currency" example:"usd"`
	// Cents. amount_due is what the card is asked for after credit.
	Total           int64 `json:"total" example:"15000"`
	AmountDue       int64 `json:"amount_due" example:"15000"`
	AmountPaid      int64 `json:"amount_paid" example:"0"`
	AmountRemaining int64 `json:"amount_remaining" example:"15000"`
	// Stripe's page where the customer can see and pay it, and the PDF.
	HostedInvoiceURL string     `json:"hosted_invoice_url" example:"https://invoice.stripe.com/i/acct_1/test_x"`
	InvoicePDF       string     `json:"invoice_pdf" example:"https://pay.stripe.com/invoice/acct_1/test_x/pdf"`
	PeriodStart      *time.Time `json:"period_start"`
	PeriodEnd        *time.Time `json:"period_end"`
	CreatedAt        time.Time  `json:"created_at" example:"2026-10-07T11:26:26Z"`
	PaidAt           *time.Time `json:"paid_at"`
	// Only when one invoice is read: its lines.
	Lines []InvoiceLineResponse `json:"lines,omitempty"`
}

type ListInvoicesResponse struct {
	Items    []InvoiceResponse `json:"items"`
	Page     int               `json:"page" example:"1"`
	PageSize int               `json:"page_size" example:"20"`
	Total    int               `json:"total" example:"12"`
}

type InvoicePaymentResponse struct {
	Invoice InvoiceResponse `json:"invoice"`
	// Not null: the bank wants the card holder to confirm (3-D Secure).
	// Finish with stripe.confirmPayment in the browser.
	Payment *PaymentDueResponse `json:"payment"`
}

type CreateChargeRequest struct {
	// What the company sees on the invoice.
	Description string `json:"description" example:"ELD installation, 3 trucks"`
	Amount      int64  `json:"amount" example:"15000"`
	// true: charge the default card now. false: Stripe collects it on its own schedule.
	ChargeNow bool `json:"charge_now" example:"true"`
}

type ChargeResponse struct {
	Invoice InvoiceResponse `json:"invoice"`
	// Why charging now failed (the bank's words); the invoice stays open
	// and Stripe retries it. Empty = paid (or not charged now).
	PaymentError string `json:"payment_error,omitempty" example:"Your card has insufficient funds."`
}

type AddCreditRequest struct {
	Amount      int64  `json:"amount" example:"2500"`
	Description string `json:"description" example:"Service outage on 3 Oct"`
}

type BalanceTransactionResponse struct {
	ID string `json:"id" example:"cbtxn_1Qabc"`
	// adjustment (credit given), applied_to_invoice, credit_note, ...
	Type string `json:"type" example:"adjustment"`
	// Cents; negative = credit added.
	Amount        int64     `json:"amount" example:"-2500"`
	EndingBalance int64     `json:"ending_balance" example:"-2500"`
	Description   string    `json:"description" example:"Service outage on 3 Oct"`
	InvoiceID     string    `json:"stripe_invoice_id,omitempty" example:"in_1Qabc"`
	CreatedAt     time.Time `json:"created_at" example:"2026-10-07T11:26:26Z"`
}

type BalanceResponse struct {
	// Cents. Negative = credit the company has (pays its next invoices).
	Balance      int64                        `json:"balance" example:"-2500"`
	Transactions []BalanceTransactionResponse `json:"transactions"`
}

type CreateCreditNoteRequest struct {
	Amount int64 `json:"amount" example:"5000"`
	// Paid invoice: true = refund to the card; false = credit to the balance.
	Refund bool `json:"refund" example:"true"`
	// duplicate, fraudulent, order_change, product_unsatisfactory
	Reason string `json:"reason,omitempty" example:"order_change"`
	Memo   string `json:"memo,omitempty" example:"Two trucks were billed twice"`
}

type CreditNoteResponse struct {
	ID           string    `json:"id" example:"cn_1Qabc"`
	Number       string    `json:"number" example:"ABCD-0001-CN-01"`
	Amount       int64     `json:"amount" example:"5000"`
	RefundAmount int64     `json:"refund_amount" example:"5000"`
	CreditAmount int64     `json:"credit_amount" example:"0"`
	Reason       string    `json:"reason" example:"order_change"`
	Memo         string    `json:"memo" example:"Two trucks were billed twice"`
	CreatedAt    time.Time `json:"created_at" example:"2026-10-07T11:26:26Z"`
}

func newInvoiceResponse(inv *model.Invoice) InvoiceResponse {
	out := InvoiceResponse{
		ID: inv.ID, CompanyID: inv.CompanyID, Number: inv.Number, Status: inv.Status, BillingReason: inv.BillingReason,
		Description: inv.Description, Currency: inv.Currency, Total: inv.Total, AmountDue: inv.AmountDue,
		AmountPaid: inv.AmountPaid, AmountRemaining: inv.AmountRemaining,
		HostedInvoiceURL: inv.HostedInvoiceURL, InvoicePDF: inv.InvoicePDF,
		PeriodStart: utc(inv.PeriodStart), PeriodEnd: utc(inv.PeriodEnd),
		CreatedAt: inv.StripeCreatedAt.UTC(), PaidAt: utc(inv.PaidAt),
	}
	for _, l := range inv.Lines {
		out.Lines = append(out.Lines, InvoiceLineResponse{Description: l.Description, Amount: l.Amount, Quantity: l.Quantity, Proration: l.Proration})
	}
	return out
}

func newCreditNoteResponse(cn *model.CreditNote) CreditNoteResponse {
	return CreditNoteResponse{ID: cn.ID, Number: cn.Number, Amount: cn.Amount, RefundAmount: cn.RefundAmount,
		CreditAmount: cn.CreditAmount, Reason: cn.Reason, Memo: cn.Memo, CreatedAt: cn.CreatedAt.UTC()}
}

func newBalanceTransactionResponse(t *model.BalanceTransaction) BalanceTransactionResponse {
	return BalanceTransactionResponse{ID: t.ID, Type: t.Type, Amount: t.Amount, EndingBalance: t.EndingBalance,
		Description: t.Description, InvoiceID: t.InvoiceID, CreatedAt: t.CreatedAt.UTC()}
}

// ---------- Handler ----------

type InvoiceHandler struct {
	svc InvoiceService
}

func NewInvoiceHandler(svc InvoiceService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc}
}

var validInvoiceStatus = map[string]bool{
	model.InvoiceDraft: true, model.InvoiceOpen: true, model.InvoicePaid: true, model.InvoiceVoid: true, model.InvoiceUncollectible: true,
}

// List handles GET /api/v1/invoices.
//
//	@Summary		List invoices
//	@Description	Requires permission: invoice:read. Newest first. Company users get only their own company's (company_id is ignored).
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			company_id	query		string	false	"Filter by company (platform users only)"	format(uuid)
//	@Param			status		query		string	false	"Filter by status"							Enums(draft, open, paid, void, uncollectible)
//	@Param			page		query		int		false	"Page number (1-based)"						default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"							default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListInvoicesResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission invoice:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/invoices [get]
func (h *InvoiceHandler) List(c *gin.Context) {
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
	v.Check(status == "" || validInvoiceStatus[status], "status", "unknown status")
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}
	res, err := h.svc.List(c.Request.Context(), p, service.InvoiceListParams{CompanyID: companyID, Status: status, Page: page, PageSize: size})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]InvoiceResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newInvoiceResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListInvoicesResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

// Get handles GET /api/v1/invoices/:id.
//
//	@Summary		Get an invoice with its lines
//	@Description	Requires permission: invoice:read. Company users: own company only.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Invoice ID"	format(uuid)
//	@Success		200	{object}	InvoiceResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission invoice:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/invoices/{id} [get]
func (h *InvoiceHandler) Get(c *gin.Context) {
	p, id, ok := principalAndID(c)
	if !ok {
		return
	}
	inv, err := h.svc.Get(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newInvoiceResponse(inv))
}

// Pay handles POST /api/v1/invoices/:id/pay.
//
//	@Summary		Pay an open invoice
//	@Description	Requires permission: invoice:pay. Company users: own company only.
//	@Description	Charges the default card now. A blocked company pays its open invoice this way to be unblocked.
//	@Description	If payment is not null, the bank wants the card holder to confirm: finish with Stripe.js.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Invoice ID"	format(uuid)
//	@Success		200	{object}	InvoicePaymentResponse
//	@Failure		400	{object}	apierror.Response	"the invoice is not open"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"card_declined, or no_payment_method (add a card first)"
//	@Failure		403	{object}	apierror.Response	"missing permission invoice:pay"
//	@Failure		404	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/invoices/{id}/pay [post]
func (h *InvoiceHandler) Pay(c *gin.Context) {
	p, id, ok := principalAndID(c)
	if !ok {
		return
	}
	res, err := h.svc.Pay(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	out := InvoicePaymentResponse{Invoice: newInvoiceResponse(res.Invoice)}
	if pd := res.Payment; pd != nil {
		out.Payment = &PaymentDueResponse{InvoiceID: pd.InvoiceID, AmountDue: pd.AmountDue, ClientSecret: pd.ClientSecret, PaymentMethodID: pd.PaymentMethodID}
	}
	c.JSON(http.StatusOK, out)
}

// Finalize handles POST /api/v1/invoices/:id/finalize.
//
//	@Summary		Finalize a draft invoice
//	@Description	Requires permission: invoice:manage (platform only). The invoice gets a number, cannot change any more, and is collected.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Invoice ID"	format(uuid)
//	@Success		200	{object}	InvoiceResponse
//	@Failure		400	{object}	apierror.Response	"not a draft"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission invoice:manage, or not a platform user"
//	@Failure		404	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/invoices/{id}/finalize [post]
func (h *InvoiceHandler) Finalize(c *gin.Context) {
	h.invoiceAction(c, h.svc.Finalize)
}

// Void handles POST /api/v1/invoices/:id/void.
//
//	@Summary		Void an open invoice
//	@Description	Requires permission: invoice:manage (platform only). Kept for the record; nothing is owed any more.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Invoice ID"	format(uuid)
//	@Success		200	{object}	InvoiceResponse
//	@Failure		400	{object}	apierror.Response	"not open"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission invoice:manage, or not a platform user"
//	@Failure		404	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/invoices/{id}/void [post]
func (h *InvoiceHandler) Void(c *gin.Context) {
	h.invoiceAction(c, h.svc.Void)
}

func (h *InvoiceHandler) invoiceAction(c *gin.Context, fn func(context.Context, auth.Principal, uuid.UUID) (*model.Invoice, error)) {
	p, id, ok := principalAndID(c)
	if !ok {
		return
	}
	inv, err := fn(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newInvoiceResponse(inv))
}

// ListCreditNotes handles GET /api/v1/invoices/:id/credit-notes.
//
//	@Summary		Credit notes of an invoice
//	@Description	Requires permission: invoice:read. Company users: own company only.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Invoice ID"	format(uuid)
//	@Success		200	{array}		CreditNoteResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission invoice:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/invoices/{id}/credit-notes [get]
func (h *InvoiceHandler) ListCreditNotes(c *gin.Context) {
	p, id, ok := principalAndID(c)
	if !ok {
		return
	}
	notes, err := h.svc.CreditNotes(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	out := make([]CreditNoteResponse, 0, len(notes))
	for i := range notes {
		out = append(out, newCreditNoteResponse(&notes[i]))
	}
	c.JSON(http.StatusOK, out)
}

// CreateCreditNote handles POST /api/v1/invoices/:id/credit-notes.
//
//	@Summary		Credit or refund part of an invoice
//	@Description	Requires permission: credit:manage (platform only).
//	@Description	Paid invoice: refund=true returns the money to the card, refund=false credits the company's balance. Open invoice: lowers what is due.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string					true	"Invoice ID"	format(uuid)
//	@Param			body	body		CreateCreditNoteRequest	true	"Credit note"
//	@Success		201		{object}	CreditNoteResponse
//	@Failure		400		{object}	apierror.Response	"validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission credit:manage, or not a platform user"
//	@Failure		404		{object}	apierror.Response
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/invoices/{id}/credit-notes [post]
func (h *InvoiceHandler) CreateCreditNote(c *gin.Context) {
	p, id, ok := principalAndID(c)
	if !ok {
		return
	}
	var req CreateCreditNoteRequest
	if !bindJSON(c, &req) {
		return
	}
	cn, err := h.svc.CreateCreditNote(c.Request.Context(), p, id, service.CreditNoteInput{
		Amount: req.Amount, Refund: req.Refund, Reason: req.Reason, Memo: req.Memo,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newCreditNoteResponse(cn))
}

// CreateCharge handles POST /api/v1/companies/:id/billing/charges.
//
//	@Summary		One-off charge
//	@Description	Requires permission: invoice:manage (platform only). A separate invoice with one line ("ELD installation").
//	@Description	Prorations waiting for the next renewal are not included. With charge_now, a decline (or no default card) is returned in payment_error and the invoice stays open.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Company ID"	format(uuid)
//	@Param			body	body		CreateChargeRequest	true	"Charge"
//	@Success		201		{object}	ChargeResponse
//	@Failure		400		{object}	apierror.Response	"validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission invoice:manage, or not a platform user"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/charges [post]
func (h *InvoiceHandler) CreateCharge(c *gin.Context) {
	p, companyID, ok := principalAndID(c)
	if !ok {
		return
	}
	var req CreateChargeRequest
	if !bindJSON(c, &req) {
		return
	}
	res, err := h.svc.CreateCharge(c.Request.Context(), p, companyID, service.ChargeInput{
		Description: req.Description, Amount: req.Amount, ChargeNow: req.ChargeNow,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, ChargeResponse{Invoice: newInvoiceResponse(res.Invoice), PaymentError: res.PaymentError})
}

// Balance handles GET /api/v1/companies/:id/billing/balance.
//
//	@Summary		Credit balance
//	@Description	Requires permission: invoice:read. Company users: own company only. Negative balance = credit that pays the next invoices.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Company ID"	format(uuid)
//	@Success		200	{object}	BalanceResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission invoice:read"
//	@Failure		404	{object}	apierror.Response	"company not found"
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/balance [get]
func (h *InvoiceHandler) Balance(c *gin.Context) {
	p, companyID, ok := principalAndID(c)
	if !ok {
		return
	}
	bal, err := h.svc.Balance(c.Request.Context(), p, companyID)
	if err != nil {
		handleError(c, err)
		return
	}
	out := BalanceResponse{Balance: bal.Balance, Transactions: make([]BalanceTransactionResponse, 0, len(bal.Transactions))}
	for i := range bal.Transactions {
		out.Transactions = append(out.Transactions, newBalanceTransactionResponse(&bal.Transactions[i]))
	}
	c.JSON(http.StatusOK, out)
}

// AddCredit handles POST /api/v1/companies/:id/billing/credits.
//
//	@Summary		Give credit
//	@Description	Requires permission: credit:manage (platform only). Added to the balance; Stripe uses it for the next invoices.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Company ID"	format(uuid)
//	@Param			body	body		AddCreditRequest	true	"Credit"
//	@Success		201		{object}	BalanceTransactionResponse
//	@Failure		400		{object}	apierror.Response	"validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission credit:manage, or not a platform user"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/companies/{id}/billing/credits [post]
func (h *InvoiceHandler) AddCredit(c *gin.Context) {
	p, companyID, ok := principalAndID(c)
	if !ok {
		return
	}
	var req AddCreditRequest
	if !bindJSON(c, &req) {
		return
	}
	tx, err := h.svc.AddCredit(c.Request.Context(), p, companyID, service.CreditInput{Amount: req.Amount, Description: req.Description})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newBalanceTransactionResponse(tx))
}

// principalAndID reads the caller and the :id path parameter; on failure
// the response is written and ok is false.
func principalAndID(c *gin.Context) (auth.Principal, uuid.UUID, bool) {
	p, ok := principal(c)
	if !ok {
		return auth.Principal{}, uuid.Nil, false
	}
	id, ok := parseUUIDParam(c, "id")
	return p, id, ok
}
