package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// InvoiceStore is what InvoiceService needs from the data layer.
type InvoiceStore interface {
	Upsert(ctx context.Context, inv model.Invoice) (*model.Invoice, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Invoice, error)
	GetByStripeID(ctx context.Context, stripeID string) (*model.Invoice, error)
	List(ctx context.Context, f model.InvoiceFilter, limit, offset int) ([]model.Invoice, int, error)
}

// InvoiceCompanies finds the company of a Stripe customer.
type InvoiceCompanies interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Company, error)
	GetByStripeCustomerID(ctx context.Context, customerID string) (*model.Company, error)
}

// InvoiceCustomers is what InvoiceService needs from CustomerService.
type InvoiceCustomers interface {
	EnsureCustomer(ctx context.Context, p auth.Principal, companyID uuid.UUID) (string, error)
	ListPaymentMethods(ctx context.Context, p auth.Principal, companyID uuid.UUID) ([]model.PaymentMethod, error)
}

// InvoiceGateway is what InvoiceService needs from Stripe (stripegw.Invoices).
type InvoiceGateway interface {
	GetInvoice(ctx context.Context, id string, withLines bool) (*model.StripeInvoiceSnapshot, error)
	PayInvoice(ctx context.Context, id string) (*model.StripeInvoiceSnapshot, error)
	FinalizeInvoice(ctx context.Context, id string) (*model.StripeInvoiceSnapshot, error)
	VoidInvoice(ctx context.Context, id string) (*model.StripeInvoiceSnapshot, error)
	CreateOneOffCharge(ctx context.Context, in model.StripeOneOffCharge) (*model.StripeInvoiceSnapshot, error)
	Balance(ctx context.Context, customerID string) (*model.CustomerBalance, error)
	AddCredit(ctx context.Context, customerID string, amount int64, description, idempotencyKey string) (*model.BalanceTransaction, error)
	CreateCreditNote(ctx context.Context, in model.StripeCreditNoteRequest) (*model.CreditNote, error)
	ListCreditNotes(ctx context.Context, invoiceID string) ([]model.CreditNote, error)
}

const (
	chargeDescriptionMaxLen = 500
	// Stripe's maximum for USD, as for prices.
	chargeMaxAmount = 99_999_999
)

// Reasons Stripe accepts on a credit note.
var creditNoteReasons = map[string]bool{
	"": true, "duplicate": true, "fraudulent": true, "order_change": true, "product_unsatisfactory": true,
}

type InvoiceListParams struct {
	CompanyID *uuid.UUID // platform users only
	Status    string
	Page      int
	PageSize  int
}

type InvoicePage struct {
	Items    []model.Invoice
	Total    int
	Page     int
	PageSize int
}

// InvoicePayment is the result of paying an invoice: paid, or (Payment
// set) the bank wants the card holder to confirm in the browser.
type InvoicePayment struct {
	Invoice *model.Invoice
	Payment *PaymentDue
}

type ChargeInput struct {
	Description string
	Amount      int64 // cents
	ChargeNow   bool  // false = finalized, Stripe collects it on its own schedule
}

// ChargeResult: the one-off invoice, and why charging it now failed (the
// invoice exists anyway and stays open; Stripe retries it).
type ChargeResult struct {
	Invoice      *model.Invoice
	PaymentError string
}

type CreditInput struct {
	Amount      int64 // cents, > 0
	Description string
}

type CreditNoteInput struct {
	Amount int64 // cents, > 0
	Refund bool  // paid invoice: true = back to the card, false = to the balance
	Reason string
	Memo   string
}

// InvoiceService shows companies their invoices and lets them pay; the
// platform also finalizes, voids, charges one-off amounts and gives credit.
type InvoiceService struct {
	store     InvoiceStore
	companies InvoiceCompanies
	customers InvoiceCustomers
	gateway   InvoiceGateway
	subs      SubscriptionSyncer
	audit     *Auditor
}

func NewInvoiceService(store InvoiceStore, companies InvoiceCompanies, customers InvoiceCustomers,
	gateway InvoiceGateway, subs SubscriptionSyncer, audit *Auditor) *InvoiceService {
	return &InvoiceService{store: store, companies: companies, customers: customers, gateway: gateway, subs: subs, audit: audit}
}

// ---------- reading ----------

func (s *InvoiceService) List(ctx context.Context, p auth.Principal, params InvoiceListParams) (*InvoicePage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})
	f := model.InvoiceFilter{CompanyID: params.CompanyID, Status: params.Status}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID
	}
	items, total, err := s.store.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &InvoicePage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

// Get returns one invoice with its lines (read from Stripe).
func (s *InvoiceService) Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error) {
	inv, err := s.inScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	snap, err := s.gateway.GetInvoice(ctx, inv.StripeInvoiceID, true)
	if err != nil {
		return nil, err
	}
	inv.Lines = snap.Lines
	return inv, nil
}

// CreditNotes lists the credit notes of an invoice.
func (s *InvoiceService) CreditNotes(ctx context.Context, p auth.Principal, id uuid.UUID) ([]model.CreditNote, error) {
	inv, err := s.inScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return s.gateway.ListCreditNotes(ctx, inv.StripeInvoiceID)
}

// Balance returns the company's credit balance at Stripe. A company
// without billing has none: reading never creates a Stripe customer.
func (s *InvoiceService) Balance(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.CustomerBalance, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	c, err := s.companies.GetByID(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if c.StripeCustomerID == nil {
		return &model.CustomerBalance{Transactions: []model.BalanceTransaction{}}, nil
	}
	return s.gateway.Balance(ctx, *c.StripeCustomerID)
}

// ---------- paying ----------

// Pay charges an open invoice now with the company's default card. This
// is how a company blocked for unpaid invoices gets going again: once the
// subscription's invoice is paid, Stripe reactivates the subscription.
func (s *InvoiceService) Pay(ctx context.Context, p auth.Principal, id uuid.UUID) (*InvoicePayment, error) {
	inv, err := s.inScope(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if inv.Status != model.InvoiceOpen {
		return nil, &validation.ValidationError{Fields: map[string]string{"invoice": "only an open invoice can be paid"}}
	}

	snap, err := s.gateway.PayInvoice(ctx, inv.StripeInvoiceID)
	if errors.Is(err, model.ErrAuthenticationRequired) {
		// The bank wants the card holder: hand the browser what it needs.
		return s.needsConfirmation(ctx, p, inv)
	}
	if err != nil {
		return nil, err
	}

	saved, err := s.saveAndRecord(ctx, p, inv.ID, inv.CompanyID, snap, "invoice.pay", nil)
	if err != nil {
		return nil, err
	}
	s.syncSubscription(ctx, saved)
	return &InvoicePayment{Invoice: saved}, nil
}

func (s *InvoiceService) needsConfirmation(ctx context.Context, p auth.Principal, inv *model.Invoice) (*InvoicePayment, error) {
	snap, err := s.gateway.GetInvoice(ctx, inv.StripeInvoiceID, false)
	if err != nil {
		return nil, err
	}
	saved, err := s.store.Upsert(ctx, fromInvoiceSnapshot(inv.ID, inv.CompanyID, snap))
	if err != nil {
		return nil, err
	}
	card := ""
	if cards, err := s.customers.ListPaymentMethods(ctx, p, inv.CompanyID); err == nil {
		for _, c := range cards {
			if c.IsDefault {
				card = c.ID
			}
		}
	}
	return &InvoicePayment{Invoice: saved, Payment: &PaymentDue{
		InvoiceID: snap.StripeInvoiceID, AmountDue: snap.AmountRemaining, ClientSecret: snap.ClientSecret, PaymentMethodID: card,
	}}, nil
}

// syncSubscription refreshes the invoice's subscription at once (paying
// can unblock the company). Webhooks do it too; a failure here only means
// waiting for them.
func (s *InvoiceService) syncSubscription(ctx context.Context, inv *model.Invoice) {
	if inv.StripeSubscriptionID != "" {
		_, _ = s.subs.SyncByStripeID(ctx, inv.StripeSubscriptionID)
	}
}

// ---------- platform actions ----------

// Finalize turns a draft invoice into a real, numbered bill.
func (s *InvoiceService) Finalize(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error) {
	inv, err := s.platformInvoice(ctx, p, id, model.InvoiceDraft, "only a draft invoice can be finalized")
	if err != nil {
		return nil, err
	}
	snap, err := s.gateway.FinalizeInvoice(ctx, inv.StripeInvoiceID)
	if err != nil {
		return nil, err
	}
	return s.saveAndRecord(ctx, p, inv.ID, inv.CompanyID, snap, "invoice.finalize", nil)
}

// Void cancels an open invoice: nothing is owed any more.
func (s *InvoiceService) Void(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error) {
	inv, err := s.platformInvoice(ctx, p, id, model.InvoiceOpen, "only an open invoice can be voided")
	if err != nil {
		return nil, err
	}
	snap, err := s.gateway.VoidInvoice(ctx, inv.StripeInvoiceID)
	if err != nil {
		return nil, err
	}
	saved, err := s.saveAndRecord(ctx, p, inv.ID, inv.CompanyID, snap, "invoice.void", nil)
	if err != nil {
		return nil, err
	}
	s.syncSubscription(ctx, saved)
	return saved, nil
}

// CreateCharge bills a company a one-off amount ("ELD installation"), as a
// separate invoice. With ChargeNow the default card is charged at once; if
// that fails, the invoice stays open (Stripe retries it) and the reason is
// returned with it.
func (s *InvoiceService) CreateCharge(ctx context.Context, p auth.Principal, companyID uuid.UUID, in ChargeInput) (*ChargeResult, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	desc := strings.TrimSpace(in.Description)
	var v validation.Validator
	v.Check(in.Amount >= 1 && in.Amount <= chargeMaxAmount, "amount", "must be between 1 and 99999999 (cents)")
	v.Check(desc != "", "description", "is required: it is what the company sees on the invoice")
	v.Check(utf8.RuneCountInString(desc) <= chargeDescriptionMaxLen, "description", "must be at most 500 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	customerID, err := s.customers.EnsureCustomer(ctx, p, companyID)
	if err != nil {
		return nil, err
	}
	chargeID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	snap, err := s.gateway.CreateOneOffCharge(ctx, model.StripeOneOffCharge{
		ID: chargeID, CompanyID: companyID, CustomerID: customerID, Description: desc, Amount: in.Amount,
	})
	if err != nil {
		return nil, err
	}
	saved, err := s.saveAndRecord(ctx, p, chargeID, companyID, snap, "invoice.charge",
		model.AuditChanges{"description": {New: desc}, "amount": {New: in.Amount}})
	if err != nil {
		return nil, err
	}
	res := &ChargeResult{Invoice: saved}
	if !in.ChargeNow {
		return res, nil
	}

	paid, err := s.gateway.PayInvoice(ctx, saved.StripeInvoiceID)
	switch {
	case errors.Is(err, model.ErrCardDeclined), errors.Is(err, model.ErrAuthenticationRequired):
		res.PaymentError = strings.TrimPrefix(err.Error(), model.ErrCardDeclined.Error()+": ")
		return res, nil
	case err != nil:
		return nil, err
	}
	if res.Invoice, err = s.store.Upsert(ctx, fromInvoiceSnapshot(saved.ID, companyID, paid)); err != nil {
		return nil, err
	}
	return res, nil
}

// AddCredit gives a company credit (e.g. after an outage). Stripe uses it
// automatically for the next invoices.
func (s *InvoiceService) AddCredit(ctx context.Context, p auth.Principal, companyID uuid.UUID, in CreditInput) (*model.BalanceTransaction, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	desc := strings.TrimSpace(in.Description)
	var v validation.Validator
	v.Check(in.Amount >= 1 && in.Amount <= chargeMaxAmount, "amount", "must be between 1 and 99999999 (cents)")
	v.Check(desc != "", "description", "is required: say why the credit is given")
	v.Check(utf8.RuneCountInString(desc) <= chargeDescriptionMaxLen, "description", "must be at most 500 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	customerID, err := s.customers.EnsureCustomer(ctx, p, companyID)
	if err != nil {
		return nil, err
	}
	key, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	tx, err := s.gateway.AddCredit(ctx, customerID, in.Amount, desc, "credit-"+key.String())
	if err != nil {
		return nil, err
	}
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		return s.audit.Record(ctx, p, model.AuditEntry{
			Action: "company.credit", Entity: model.AuditCompany, EntityID: companyID.String(), CompanyID: &companyID,
			Changes: model.AuditChanges{"credit": {New: in.Amount}, "description": {New: desc}, "balance": {New: tx.EndingBalance}},
		})
	})
	return tx, err
}

// CreateCreditNote reduces an invoice: on a paid invoice the amount is
// refunded to the card or credited to the balance; on an open one it
// lowers what is still due.
func (s *InvoiceService) CreateCreditNote(ctx context.Context, p auth.Principal, id uuid.UUID, in CreditNoteInput) (*model.CreditNote, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	inv, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	var v validation.Validator
	v.Check(inv.Status == model.InvoicePaid || inv.Status == model.InvoiceOpen, "invoice", "only a paid or open invoice can be credited")
	v.Check(in.Amount >= 1 && in.Amount <= inv.Total, "amount", "must be between 1 and the invoice total")
	v.Check(creditNoteReasons[in.Reason], "reason", "must be duplicate, fraudulent, order_change or product_unsatisfactory")
	v.Check(!in.Refund || inv.Status == model.InvoicePaid, "refund", "only a paid invoice can be refunded")
	v.Check(utf8.RuneCountInString(in.Memo) <= chargeDescriptionMaxLen, "memo", "must be at most 500 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	noteID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	cn, err := s.gateway.CreateCreditNote(ctx, model.StripeCreditNoteRequest{
		ID: noteID, InvoiceID: inv.StripeInvoiceID, Amount: in.Amount,
		Paid: inv.Status == model.InvoicePaid, Refund: in.Refund, Reason: in.Reason, Memo: strings.TrimSpace(in.Memo),
	})
	if err != nil {
		return nil, err
	}
	snap, err := s.gateway.GetInvoice(ctx, inv.StripeInvoiceID, false)
	if err != nil {
		return nil, err
	}
	_, err = s.saveAndRecord(ctx, p, inv.ID, inv.CompanyID, snap, "invoice.credit_note", model.AuditChanges{
		"credit_note": {New: cn.Number}, "amount": {New: cn.Amount},
		"refunded": {New: cn.RefundAmount}, "credited": {New: cn.CreditAmount},
	})
	return cn, err
}

// ---------- webhooks ----------

// SyncByStripeID reads one invoice from Stripe and saves our copy. Called
// by webhooks; ErrNotOurs for invoices of none of our companies.
func (s *InvoiceService) SyncByStripeID(ctx context.Context, stripeID string) (*model.Invoice, error) {
	snap, err := s.gateway.GetInvoice(ctx, stripeID, false)
	if errors.Is(err, model.ErrNotFound) {
		return nil, ErrNotOurs
	}
	if err != nil {
		return nil, err
	}
	if existing, err := s.store.GetByStripeID(ctx, stripeID); err == nil {
		return s.store.Upsert(ctx, fromInvoiceSnapshot(existing.ID, existing.CompanyID, snap))
	} else if !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	company, err := s.companies.GetByStripeCustomerID(ctx, snap.CustomerID)
	if errors.Is(err, model.ErrNotFound) {
		return nil, ErrNotOurs
	}
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return s.store.Upsert(ctx, fromInvoiceSnapshot(id, company.ID, snap))
}

// ---------- helpers ----------

// inScope loads an invoice the caller may see: company users only their
// own company's (another company's is "not found").
func (s *InvoiceService) inScope(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Invoice, error) {
	inv, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !canAccessCompany(p, inv.CompanyID) {
		return nil, model.ErrNotFound
	}
	return inv, nil
}

func (s *InvoiceService) platformInvoice(ctx context.Context, p auth.Principal, id uuid.UUID, status, msg string) (*model.Invoice, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	inv, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if inv.Status != status {
		return nil, &validation.ValidationError{Fields: map[string]string{"invoice": msg}}
	}
	return inv, nil
}

// saveAndRecord saves our copy and records the action, together.
func (s *InvoiceService) saveAndRecord(ctx context.Context, p auth.Principal, id, companyID uuid.UUID,
	snap *model.StripeInvoiceSnapshot, action string, ch model.AuditChanges) (*model.Invoice, error) {
	var saved *model.Invoice
	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.store.Upsert(ctx, fromInvoiceSnapshot(id, companyID, snap)); err != nil {
			return err
		}
		if ch == nil {
			ch = model.AuditChanges{}
		}
		ch["status"] = model.AuditChange{New: saved.Status}
		return s.audit.Record(ctx, p, model.AuditEntry{
			Action: action, Entity: model.AuditInvoice, EntityID: saved.ID.String(), CompanyID: &companyID, Changes: ch,
		})
	})
	return saved, err
}

func fromInvoiceSnapshot(id, companyID uuid.UUID, snap *model.StripeInvoiceSnapshot) model.Invoice {
	inv := snap.Invoice
	inv.ID, inv.CompanyID = id, companyID
	return inv
}
