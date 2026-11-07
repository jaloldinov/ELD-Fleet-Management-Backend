package handler

import (
	"context"
	"encoding/json"
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

type fakeInvoiceService struct {
	gotID      uuid.UUID
	gotList    service.InvoiceListParams
	gotCharge  service.ChargeInput
	gotCredit  service.CreditInput
	gotNote    service.CreditNoteInput
	invoice    *model.Invoice
	payment    *service.PaymentDue
	payErrText string
	err        error
}

func (f *fakeInvoiceService) List(_ context.Context, _ auth.Principal, params service.InvoiceListParams) (*service.InvoicePage, error) {
	f.gotList = params
	return &service.InvoicePage{Items: []model.Invoice{*f.invoice}, Total: 1, Page: 1, PageSize: 20}, f.err
}
func (f *fakeInvoiceService) Get(_ context.Context, _ auth.Principal, id uuid.UUID) (*model.Invoice, error) {
	f.gotID = id
	return f.invoice, f.err
}
func (f *fakeInvoiceService) CreditNotes(context.Context, auth.Principal, uuid.UUID) ([]model.CreditNote, error) {
	return []model.CreditNote{}, f.err
}
func (f *fakeInvoiceService) Balance(context.Context, auth.Principal, uuid.UUID) (*model.CustomerBalance, error) {
	return &model.CustomerBalance{Balance: -2500, Transactions: []model.BalanceTransaction{{ID: "cbtxn_1", Amount: -2500}}}, f.err
}
func (f *fakeInvoiceService) Pay(_ context.Context, _ auth.Principal, id uuid.UUID) (*service.InvoicePayment, error) {
	f.gotID = id
	if f.err != nil {
		return nil, f.err
	}
	return &service.InvoicePayment{Invoice: f.invoice, Payment: f.payment}, nil
}
func (f *fakeInvoiceService) Finalize(context.Context, auth.Principal, uuid.UUID) (*model.Invoice, error) {
	return f.invoice, f.err
}
func (f *fakeInvoiceService) Void(context.Context, auth.Principal, uuid.UUID) (*model.Invoice, error) {
	return f.invoice, f.err
}
func (f *fakeInvoiceService) CreateCharge(_ context.Context, _ auth.Principal, _ uuid.UUID, in service.ChargeInput) (*service.ChargeResult, error) {
	f.gotCharge = in
	return &service.ChargeResult{Invoice: f.invoice, PaymentError: f.payErrText}, f.err
}
func (f *fakeInvoiceService) AddCredit(_ context.Context, _ auth.Principal, _ uuid.UUID, in service.CreditInput) (*model.BalanceTransaction, error) {
	f.gotCredit = in
	return &model.BalanceTransaction{ID: "cbtxn_2", Amount: -in.Amount, EndingBalance: -in.Amount}, f.err
}
func (f *fakeInvoiceService) CreateCreditNote(_ context.Context, _ auth.Principal, _ uuid.UUID, in service.CreditNoteInput) (*model.CreditNote, error) {
	f.gotNote = in
	return &model.CreditNote{ID: "cn_1", Amount: in.Amount, RefundAmount: in.Amount}, f.err
}

func sampleInvoice() *model.Invoice {
	return &model.Invoice{ID: uuid.New(), CompanyID: testCompanyID, StripeInvoiceID: "in_1", Number: "ABCD-0001",
		Status: model.InvoiceOpen, Total: 15000, AmountDue: 15000, AmountRemaining: 15000,
		StripeCreatedAt: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC),
		Lines:           []model.InvoiceLine{{Description: "12 × ELD", Amount: 30000, Quantity: 12}}}
}

func invoiceRouter(svc InvoiceService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewInvoiceHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	r.GET("/invoices", h.List)
	r.GET("/invoices/:id", h.Get)
	r.POST("/invoices/:id/pay", h.Pay)
	r.POST("/invoices/:id/credit-notes", h.CreateCreditNote)
	r.POST("/companies/:id/charges", h.CreateCharge)
	r.GET("/companies/:id/balance", h.Balance)
	r.POST("/companies/:id/credits", h.AddCredit)
	return r
}

func TestInvoiceHandler_ListAndGet(t *testing.T) {
	svc := &fakeInvoiceService{invoice: sampleInvoice()}
	r := invoiceRouter(svc)

	rec := do(r, http.MethodGet, "/invoices?status=open", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "open", svc.gotList.Status)
	assert.Equal(t, http.StatusBadRequest, do(r, http.MethodGet, "/invoices?status=lost", "").Code)

	rec = do(r, http.MethodGet, "/invoices/"+svc.invoice.ID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code)
	var got InvoiceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "ABCD-0001", got.Number)
	require.Len(t, got.Lines, 1)
}

func TestInvoiceHandler_Pay(t *testing.T) {
	svc := &fakeInvoiceService{invoice: sampleInvoice(),
		payment: &service.PaymentDue{InvoiceID: "in_1", AmountDue: 15000, ClientSecret: "pi_secret", PaymentMethodID: "pm_1"}}

	rec := do(invoiceRouter(svc), http.MethodPost, "/invoices/"+svc.invoice.ID.String()+"/pay", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got InvoicePaymentResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.Payment, "3-D Secure: the browser finishes it")
	assert.Equal(t, "pi_secret", got.Payment.ClientSecret)

	svc.err = model.ErrAuthenticationRequired
	rec = do(invoiceRouter(svc), http.MethodPost, "/invoices/"+svc.invoice.ID.String()+"/pay", "")
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	assert.Equal(t, "authentication_required", decodeError(t, rec).Code)

	// No card: the customer's to fix (402), not "try again later" (502).
	svc.err = model.ErrNoPaymentMethod
	rec = do(invoiceRouter(svc), http.MethodPost, "/invoices/"+svc.invoice.ID.String()+"/pay", "")
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
	body := decodeError(t, rec)
	assert.Equal(t, "no_payment_method", body.Code)
	assert.Contains(t, body.Message, "add one on the billing page")
}

func TestInvoiceHandler_ChargeCreditBalance(t *testing.T) {
	svc := &fakeInvoiceService{invoice: sampleInvoice(), payErrText: "Your card has insufficient funds."}
	r := invoiceRouter(svc)
	base := "/companies/" + testCompanyID.String()

	rec := do(r, http.MethodPost, base+"/charges", `{"description":"ELD installation","amount":15000,"charge_now":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, service.ChargeInput{Description: "ELD installation", Amount: 15000, ChargeNow: true}, svc.gotCharge)
	assert.Contains(t, rec.Body.String(), `"payment_error":"Your card has insufficient funds."`)

	rec = do(r, http.MethodPost, base+"/credits", `{"amount":2500,"description":"Outage"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"amount":-2500`, "credit is a negative balance")

	rec = do(r, http.MethodGet, base+"/balance", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"balance":-2500`)

	rec = do(r, http.MethodPost, "/invoices/"+svc.invoice.ID.String()+"/credit-notes", `{"amount":5000,"refund":true,"reason":"order_change"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, svc.gotNote.Refund)
}
