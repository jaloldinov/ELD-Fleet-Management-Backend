package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// ---------- fakes ----------

type memInvoiceStore struct {
	byID  map[uuid.UUID]*model.Invoice
	listF model.InvoiceFilter
}

func (m *memInvoiceStore) Upsert(_ context.Context, inv model.Invoice) (*model.Invoice, error) {
	for id, ex := range m.byID {
		if ex.StripeInvoiceID == inv.StripeInvoiceID {
			inv.ID = id
		}
	}
	m.byID[inv.ID] = &inv
	c := inv
	return &c, nil
}

func (m *memInvoiceStore) GetByID(_ context.Context, id uuid.UUID) (*model.Invoice, error) {
	inv, ok := m.byID[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *inv
	return &c, nil
}

func (m *memInvoiceStore) GetByStripeID(_ context.Context, stripeID string) (*model.Invoice, error) {
	for _, inv := range m.byID {
		if inv.StripeInvoiceID == stripeID {
			c := *inv
			return &c, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memInvoiceStore) List(_ context.Context, f model.InvoiceFilter, _, _ int) ([]model.Invoice, int, error) {
	m.listF = f
	return nil, 0, nil
}

// fakeInvoiceGateway: Stripe's invoices by id; pay/finalize/void move them.
type fakeInvoiceGateway struct {
	invoices  map[string]*model.StripeInvoiceSnapshot
	payErr    error
	credits   []int64
	notes     []model.StripeCreditNoteRequest
	charged   *model.StripeOneOffCharge
	payCalled int
}

func (f *fakeInvoiceGateway) get(id string) (*model.StripeInvoiceSnapshot, error) {
	inv, ok := f.invoices[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *inv
	return &c, nil
}

func (f *fakeInvoiceGateway) GetInvoice(_ context.Context, id string, withLines bool) (*model.StripeInvoiceSnapshot, error) {
	inv, err := f.get(id)
	if err == nil && withLines {
		inv.Lines = []model.InvoiceLine{{Description: "12 × ELD Fleet plan", Amount: 30000, Quantity: 12}}
	}
	return inv, err
}

func (f *fakeInvoiceGateway) PayInvoice(_ context.Context, id string) (*model.StripeInvoiceSnapshot, error) {
	f.payCalled++
	if f.payErr != nil {
		return nil, f.payErr
	}
	inv := f.invoices[id]
	inv.Status, inv.AmountPaid, inv.AmountRemaining = model.InvoicePaid, inv.AmountDue, 0
	return f.get(id)
}

func (f *fakeInvoiceGateway) FinalizeInvoice(_ context.Context, id string) (*model.StripeInvoiceSnapshot, error) {
	f.invoices[id].Status = model.InvoiceOpen
	return f.get(id)
}

func (f *fakeInvoiceGateway) VoidInvoice(_ context.Context, id string) (*model.StripeInvoiceSnapshot, error) {
	f.invoices[id].Status = model.InvoiceVoid
	return f.get(id)
}

func (f *fakeInvoiceGateway) CreateOneOffCharge(_ context.Context, in model.StripeOneOffCharge) (*model.StripeInvoiceSnapshot, error) {
	f.charged = &in
	snap := &model.StripeInvoiceSnapshot{CustomerID: in.CustomerID, Invoice: model.Invoice{
		StripeInvoiceID: "in_charge", Status: model.InvoiceOpen, BillingReason: "manual", Description: in.Description,
		Total: in.Amount, AmountDue: in.Amount, AmountRemaining: in.Amount,
	}}
	f.invoices["in_charge"] = snap
	return f.get("in_charge")
}

func (f *fakeInvoiceGateway) Balance(context.Context, string) (*model.CustomerBalance, error) {
	return &model.CustomerBalance{Balance: -2500, Transactions: []model.BalanceTransaction{}}, nil
}

func (f *fakeInvoiceGateway) AddCredit(_ context.Context, _ string, amount int64, _, _ string) (*model.BalanceTransaction, error) {
	f.credits = append(f.credits, amount)
	return &model.BalanceTransaction{ID: "cbtxn_1", Amount: -amount, EndingBalance: -amount}, nil
}

func (f *fakeInvoiceGateway) CreateCreditNote(_ context.Context, in model.StripeCreditNoteRequest) (*model.CreditNote, error) {
	f.notes = append(f.notes, in)
	cn := &model.CreditNote{ID: "cn_1", Number: "ABCD-CN-1", Amount: in.Amount}
	if in.Refund {
		cn.RefundAmount = in.Amount
	} else if in.Paid {
		cn.CreditAmount = in.Amount
	}
	return cn, nil
}

func (f *fakeInvoiceGateway) ListCreditNotes(context.Context, string) ([]model.CreditNote, error) {
	return []model.CreditNote{}, nil
}

type invFixture struct {
	svc     *InvoiceService
	store   *memInvoiceStore
	gw      *fakeInvoiceGateway
	subs    *fakeSyncer
	audit   *memAuditStore
	openInv *model.Invoice // company A's open renewal invoice, $150
	paidInv *model.Invoice // company A's paid invoice, $100
	draft   *model.Invoice
	theirs  *model.Invoice // another company's
}

func newInvFixture(t *testing.T) invFixture {
	t.Helper()
	f := invFixture{
		store: &memInvoiceStore{byID: map[uuid.UUID]*model.Invoice{}},
		gw:    &fakeInvoiceGateway{invoices: map[string]*model.StripeInvoiceSnapshot{}},
		subs:  &fakeSyncer{},
	}
	add := func(stripeID, status string, company uuid.UUID, total int64) *model.Invoice {
		snap := &model.StripeInvoiceSnapshot{CustomerID: "cus_A", Invoice: model.Invoice{
			StripeInvoiceID: stripeID, StripeSubscriptionID: "sub_1", Status: status,
			Total: total, AmountDue: total, AmountRemaining: total,
		}}
		snap.ClientSecret = "pi_secret"
		f.gw.invoices[stripeID] = snap
		inv := snap.Invoice
		inv.ID, inv.CompanyID = uuid.New(), company
		f.store.byID[inv.ID] = &inv
		return &inv
	}
	f.openInv = add("in_open", model.InvoiceOpen, companyA, 15000)
	f.paidInv = add("in_paid", model.InvoicePaid, companyA, 10000)
	f.draft = add("in_draft", model.InvoiceDraft, companyA, 5000)
	f.theirs = add("in_theirs", model.InvoiceOpen, companyB, 15000)

	customers := &fakeSubCustomers{cards: []model.PaymentMethod{{ID: "pm_default", IsDefault: true}}}
	billing := &memBilling{customers: map[string]uuid.UUID{"cus_A": companyA}, status: map[uuid.UUID]string{}}
	var auditor *Auditor
	auditor, f.audit = newTestAuditor()
	f.svc = NewInvoiceService(f.store, billing, customers, f.gw, f.subs, auditor)
	return f
}

// ---------- tests ----------

func TestInvoice_ListAndGet_Scope(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()

	_, err := f.svc.List(ctx, companyAUser, InvoiceListParams{CompanyID: &companyB})
	require.NoError(t, err)
	assert.Equal(t, &companyA, f.store.listF.CompanyID, "company users see only their own")

	got, err := f.svc.Get(ctx, companyAUser, f.openInv.ID)
	require.NoError(t, err)
	require.Len(t, got.Lines, 1, "lines come from Stripe")

	_, err = f.svc.Get(ctx, companyAUser, f.theirs.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = f.svc.Get(ctx, platformAdmin, f.theirs.ID)
	assert.NoError(t, err)
}

func TestInvoice_Pay(t *testing.T) {
	f := newInvFixture(t)

	res, err := f.svc.Pay(context.Background(), companyAUser, f.openInv.ID)

	require.NoError(t, err)
	assert.Equal(t, model.InvoicePaid, res.Invoice.Status)
	assert.Nil(t, res.Payment)
	assert.Equal(t, []string{"sub_1"}, f.subs.synced, "paying can unblock the company: refresh the subscription now")
	assert.Equal(t, "invoice.pay", f.audit.last(t).Action)
	f.audit.requireAllInTx(t)
}

func TestInvoice_Pay_Rules(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()

	_, err := f.svc.Pay(ctx, companyAUser, f.paidInv.ID)
	requireValidationField(t, err, "invoice")
	_, err = f.svc.Pay(ctx, companyAUser, f.theirs.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Zero(t, f.gw.payCalled)

	f.gw.payErr = fmt.Errorf("%w: Your card was declined.", model.ErrCardDeclined)
	_, err = f.svc.Pay(ctx, companyAUser, f.openInv.ID)
	assert.ErrorIs(t, err, model.ErrCardDeclined)
}

func TestInvoice_Pay_BankWantsTheCardHolder(t *testing.T) {
	f := newInvFixture(t)
	f.gw.payErr = model.ErrAuthenticationRequired

	res, err := f.svc.Pay(context.Background(), companyAUser, f.openInv.ID)

	require.NoError(t, err, "not a failure: the browser finishes it")
	require.NotNil(t, res.Payment)
	assert.Equal(t, PaymentDue{InvoiceID: "in_open", AmountDue: 15000, ClientSecret: "pi_secret", PaymentMethodID: "pm_default"}, *res.Payment)
}

func TestInvoice_FinalizeAndVoid_PlatformOnly(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()

	_, err := f.svc.Finalize(ctx, companyAUser, f.draft.ID)
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = f.svc.Finalize(ctx, platformAdmin, f.openInv.ID)
	requireValidationField(t, err, "invoice")

	inv, err := f.svc.Finalize(ctx, platformAdmin, f.draft.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InvoiceOpen, inv.Status)

	inv, err = f.svc.Void(ctx, platformAdmin, f.openInv.ID)
	require.NoError(t, err)
	assert.Equal(t, model.InvoiceVoid, inv.Status)
	assert.Equal(t, "invoice.void", f.audit.last(t).Action)
	_, err = f.svc.Void(ctx, platformAdmin, f.paidInv.ID)
	requireValidationField(t, err, "invoice")
}

func TestInvoice_CreateCharge(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()

	_, err := f.svc.CreateCharge(ctx, companyAUser, companyA, ChargeInput{Description: "x", Amount: 100})
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = f.svc.CreateCharge(ctx, platformAdmin, companyA, ChargeInput{Description: " ", Amount: 0})
	requireValidationField(t, err, "amount")
	requireValidationField(t, err, "description")

	res, err := f.svc.CreateCharge(ctx, platformAdmin, companyA, ChargeInput{Description: " ELD installation ", Amount: 15000, ChargeNow: true})
	require.NoError(t, err)
	assert.Equal(t, "ELD installation", f.gw.charged.Description)
	assert.Equal(t, "cus_A", f.gw.charged.CustomerID)
	assert.Equal(t, model.InvoicePaid, res.Invoice.Status)
	assert.Empty(t, res.PaymentError)
	assert.Equal(t, f.gw.charged.ID, res.Invoice.ID, "our id = the charge id sent to Stripe")
	assert.Contains(t, f.audit.actions(), "invoice.charge")
}

func TestInvoice_CreateCharge_DeclinedStaysOpen(t *testing.T) {
	f := newInvFixture(t)
	f.gw.payErr = fmt.Errorf("%w: Your card has insufficient funds.", model.ErrCardDeclined)

	res, err := f.svc.CreateCharge(context.Background(), platformAdmin, companyA,
		ChargeInput{Description: "ELD installation", Amount: 15000, ChargeNow: true})

	require.NoError(t, err, "the invoice exists; the decline is reported with it")
	assert.Equal(t, model.InvoiceOpen, res.Invoice.Status)
	assert.Equal(t, "Your card has insufficient funds.", res.PaymentError)
}

func TestInvoice_AddCredit(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()

	_, err := f.svc.AddCredit(ctx, companyAUser, companyA, CreditInput{Amount: 100, Description: "x"})
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = f.svc.AddCredit(ctx, platformAdmin, companyA, CreditInput{Amount: 100})
	requireValidationField(t, err, "description")

	tx, err := f.svc.AddCredit(ctx, platformAdmin, companyA, CreditInput{Amount: 2500, Description: "Outage on 3 Oct"})
	require.NoError(t, err)
	assert.Equal(t, []int64{2500}, f.gw.credits)
	assert.Equal(t, int64(-2500), tx.EndingBalance)
	e := f.audit.last(t)
	assert.Equal(t, "company.credit", e.Action)
	assert.Equal(t, int64(2500), e.Changes["credit"].New)
}

func TestInvoice_CreditNote(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()

	_, err := f.svc.CreateCreditNote(ctx, platformAdmin, f.openInv.ID, CreditNoteInput{Amount: 1000, Refund: true})
	requireValidationField(t, err, "refund") // nothing paid yet, nothing to refund
	_, err = f.svc.CreateCreditNote(ctx, platformAdmin, f.paidInv.ID, CreditNoteInput{Amount: 999_999})
	requireValidationField(t, err, "amount")
	_, err = f.svc.CreateCreditNote(ctx, platformAdmin, f.paidInv.ID, CreditNoteInput{Amount: 100, Reason: "because"})
	requireValidationField(t, err, "reason")
	_, err = f.svc.CreateCreditNote(ctx, platformAdmin, f.draft.ID, CreditNoteInput{Amount: 100})
	requireValidationField(t, err, "invoice")
	_, err = f.svc.CreateCreditNote(ctx, companyAUser, f.paidInv.ID, CreditNoteInput{Amount: 100})
	assert.ErrorIs(t, err, model.ErrForbidden)

	cn, err := f.svc.CreateCreditNote(ctx, platformAdmin, f.paidInv.ID, CreditNoteInput{Amount: 5000, Refund: true, Reason: "order_change"})
	require.NoError(t, err)
	assert.Equal(t, int64(5000), cn.RefundAmount)
	require.Len(t, f.gw.notes, 1)
	assert.True(t, f.gw.notes[0].Paid)
	assert.Equal(t, "invoice.credit_note", f.audit.last(t).Action)
}

func TestInvoice_SyncByStripeID(t *testing.T) {
	f := newInvFixture(t)
	ctx := context.Background()
	now := time.Now()
	f.gw.invoices["in_new"] = &model.StripeInvoiceSnapshot{CustomerID: "cus_A", Invoice: model.Invoice{
		StripeInvoiceID: "in_new", Status: model.InvoiceDraft, StripeCreatedAt: now}}
	f.gw.invoices["in_stranger"] = &model.StripeInvoiceSnapshot{CustomerID: "cus_other", Invoice: model.Invoice{StripeInvoiceID: "in_stranger"}}

	inv, err := f.svc.SyncByStripeID(ctx, "in_new")
	require.NoError(t, err)
	assert.Equal(t, companyA, inv.CompanyID, "found by its Stripe customer")

	f.gw.invoices["in_new"].Status = model.InvoiceOpen
	again, err := f.svc.SyncByStripeID(ctx, "in_new")
	require.NoError(t, err)
	assert.Equal(t, inv.ID, again.ID)
	assert.Equal(t, model.InvoiceOpen, again.Status)

	_, err = f.svc.SyncByStripeID(ctx, "in_stranger")
	assert.ErrorIs(t, err, ErrNotOurs)
	_, err = f.svc.SyncByStripeID(ctx, "in_deleted")
	assert.ErrorIs(t, err, ErrNotOurs)
}
