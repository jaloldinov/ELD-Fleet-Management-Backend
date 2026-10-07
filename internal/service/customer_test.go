package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// memCustomerCompanies holds companies; SetStripeCustomerID behaves like
// the SQL (first one wins).
type memCustomerCompanies struct {
	companies map[uuid.UUID]*model.Company
}

func (m *memCustomerCompanies) GetByID(_ context.Context, id uuid.UUID) (*model.Company, error) {
	c, ok := m.companies[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *memCustomerCompanies) SetStripeCustomerID(_ context.Context, id uuid.UUID, customerID string) (string, error) {
	c, ok := m.companies[id]
	if !ok {
		return "", model.ErrNotFound
	}
	if c.StripeCustomerID == nil {
		c.StripeCustomerID = &customerID
	}
	return *c.StripeCustomerID, nil
}

type fakeCustomerGateway struct {
	calls    []string
	cards    map[string]*model.PaymentMethod // by id
	defaultP string
	detached []string
	err      error
}

func (f *fakeCustomerGateway) CreateCustomer(_ context.Context, c model.Company) (string, error) {
	f.calls = append(f.calls, "CreateCustomer")
	return "cus_new", f.err
}

func (f *fakeCustomerGateway) CreateSetupIntent(_ context.Context, customerID string) (string, error) {
	f.calls = append(f.calls, "CreateSetupIntent "+customerID)
	return "seti_secret", f.err
}

func (f *fakeCustomerGateway) ListPaymentMethods(_ context.Context, customerID string) ([]model.PaymentMethod, error) {
	f.calls = append(f.calls, "ListPaymentMethods "+customerID)
	out := []model.PaymentMethod{}
	for _, c := range f.cards {
		if c.CustomerID == customerID {
			out = append(out, *c)
		}
	}
	return out, f.err
}

func (f *fakeCustomerGateway) GetPaymentMethod(_ context.Context, id string) (*model.PaymentMethod, error) {
	c, ok := f.cards[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *fakeCustomerGateway) SetDefaultPaymentMethod(_ context.Context, customerID, pmID string) error {
	f.calls = append(f.calls, "SetDefault "+customerID+" "+pmID)
	f.defaultP = pmID
	return f.err
}

func (f *fakeCustomerGateway) DetachPaymentMethod(_ context.Context, id string) error {
	f.calls = append(f.calls, "Detach "+id)
	f.detached = append(f.detached, id)
	return f.err
}

// newCustomerSvc: companyA has no billing yet; companyB is billed as
// cus_B with card pm_b. The platform admin may act on both.
func newCustomerSvc() (*CustomerService, *memCustomerCompanies, *fakeCustomerGateway, *memAuditStore) {
	cusB := "cus_B"
	companies := &memCustomerCompanies{companies: map[uuid.UUID]*model.Company{
		companyA: {ID: companyA, Name: "Acme"},
		companyB: {ID: companyB, Name: "Bolt", StripeCustomerID: &cusB},
	}}
	gw := &fakeCustomerGateway{cards: map[string]*model.PaymentMethod{
		"pm_b": {ID: "pm_b", CustomerID: "cus_B", Brand: "visa", Last4: "4242"},
	}}
	audit, store := newTestAuditor()
	return NewCustomerService(companies, gw, audit, "pk_test_123"), companies, gw, store
}

var companyB = uuid.New()

func TestCustomer_SetupIntent_CreatesCustomerOnce(t *testing.T) {
	svc, companies, gw, store := newCustomerSvc()
	ctx := context.Background()

	si, err := svc.CreateSetupIntent(ctx, companyAUser, companyA)

	require.NoError(t, err)
	assert.Equal(t, "seti_secret", si.ClientSecret)
	assert.Equal(t, "pk_test_123", si.PublishableKey, "the browser needs it for Stripe.js")
	assert.Equal(t, "cus_new", *companies.companies[companyA].StripeCustomerID, "linked to the company")
	assert.Equal(t, []string{"CreateCustomer", "CreateSetupIntent cus_new"}, gw.calls)
	e := store.last(t)
	assert.Equal(t, "company.billing_enable", e.Action)
	assert.Equal(t, &companyA, e.CompanyID)
	store.requireAllInTx(t)

	// Second card: the existing customer is reused.
	gw.calls = nil
	_, err = svc.CreateSetupIntent(ctx, companyAUser, companyA)
	require.NoError(t, err)
	assert.Equal(t, []string{"CreateSetupIntent cus_new"}, gw.calls)
}

func TestCustomer_EnsureCustomer_StripeFails(t *testing.T) {
	svc, companies, gw, store := newCustomerSvc()
	gw.err = errStripeDown

	_, err := svc.EnsureCustomer(context.Background(), companyAUser, companyA)

	assert.ErrorIs(t, err, model.ErrPaymentProvider)
	assert.Nil(t, companies.companies[companyA].StripeCustomerID)
	assert.Empty(t, store.entries)
}

func TestCustomer_TenantScope(t *testing.T) {
	svc, _, gw, _ := newCustomerSvc()
	ctx := context.Background()

	// A company user asking about another company: "not found", and
	// nothing reaches Stripe.
	_, err := svc.CreateSetupIntent(ctx, companyAUser, companyB)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = svc.ListPaymentMethods(ctx, companyAUser, companyB)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.ErrorIs(t, svc.RemovePaymentMethod(ctx, companyAUser, companyB, "pm_b"), model.ErrNotFound)
	assert.Empty(t, gw.calls)

	// The platform acts on any company.
	pms, err := svc.ListPaymentMethods(ctx, platformAdmin, companyB)
	require.NoError(t, err)
	assert.Len(t, pms, 1)

	_, err = svc.ListPaymentMethods(ctx, platformAdmin, uuid.New())
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestCustomer_ListWithoutBillingCreatesNothing(t *testing.T) {
	svc, companies, gw, _ := newCustomerSvc()

	pms, err := svc.ListPaymentMethods(context.Background(), companyAUser, companyA)

	require.NoError(t, err)
	assert.NotNil(t, pms)
	assert.Empty(t, pms)
	assert.Empty(t, gw.calls, "reading never creates a Stripe customer")
	assert.Nil(t, companies.companies[companyA].StripeCustomerID)
}

func TestCustomer_SetDefault(t *testing.T) {
	svc, _, gw, store := newCustomerSvc()

	require.NoError(t, svc.SetDefaultPaymentMethod(context.Background(), platformAdmin, companyB, "pm_b"))

	assert.Equal(t, "pm_b", gw.defaultP)
	e := store.last(t)
	assert.Equal(t, "company.payment_method_default", e.Action)
	assert.Equal(t, "visa •••• 4242 (pm_b)", e.Changes["default_payment_method"].New, "never more than the last 4 digits")
}

func TestCustomer_Remove(t *testing.T) {
	svc, _, gw, store := newCustomerSvc()

	require.NoError(t, svc.RemovePaymentMethod(context.Background(), platformAdmin, companyB, "pm_b"))

	assert.Equal(t, []string{"pm_b"}, gw.detached)
	assert.Equal(t, "company.payment_method_remove", store.last(t).Action)
}

func TestCustomer_CardOwnership(t *testing.T) {
	svc, companies, gw, _ := newCustomerSvc()
	ctx := context.Background()
	cusA := "cus_A"
	companies.companies[companyA].StripeCustomerID = &cusA

	// pm_b belongs to company B: for company A it does not exist, even for
	// the platform admin acting on A.
	for _, p := range []struct {
		name string
		err  error
	}{
		{"other company's card", svc.RemovePaymentMethod(ctx, companyAUser, companyA, "pm_b")},
		{"other company's card (platform on A)", svc.SetDefaultPaymentMethod(ctx, platformAdmin, companyA, "pm_b")},
		{"unknown card", svc.RemovePaymentMethod(ctx, companyAUser, companyA, "pm_nope")},
		{"not a card id", svc.RemovePaymentMethod(ctx, companyAUser, companyA, "../customers/cus_B")},
	} {
		assert.ErrorIs(t, p.err, model.ErrNotFound, p.name)
	}
	assert.Empty(t, gw.detached)
	assert.Empty(t, gw.defaultP)
}

func TestCustomer_CardOfCompanyWithoutBilling(t *testing.T) {
	svc, _, gw, _ := newCustomerSvc()

	err := svc.RemovePaymentMethod(context.Background(), companyAUser, companyA, "pm_b")

	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Empty(t, gw.calls)
}
