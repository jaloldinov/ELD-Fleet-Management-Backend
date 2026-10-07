package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// ---------- fakes ----------

type memSubscriptionStore struct {
	subs  map[uuid.UUID]*model.Subscription
	listF model.SubscriptionFilter
	list  []model.Subscription // what List returns for status "active"
}

func (m *memSubscriptionStore) Upsert(_ context.Context, s model.Subscription) (*model.Subscription, error) {
	for id, existing := range m.subs {
		if existing.StripeSubscriptionID == s.StripeSubscriptionID {
			s.ID = id
		}
	}
	m.subs[s.ID] = &s
	c := s
	return &c, nil
}

func (m *memSubscriptionStore) GetByStripeID(_ context.Context, stripeID string) (*model.Subscription, error) {
	for _, s := range m.subs {
		if s.StripeSubscriptionID == stripeID {
			c := *s
			return &c, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memSubscriptionStore) GetCurrent(_ context.Context, companyID uuid.UUID) (*model.Subscription, error) {
	for _, s := range m.subs {
		if s.CompanyID == companyID && !model.SubscriptionEnded(s.Status) {
			c := *s
			return &c, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memSubscriptionStore) List(_ context.Context, f model.SubscriptionFilter, _, offset int) ([]model.Subscription, int, error) {
	m.listF = f
	if f.Status != model.SubscriptionActive || offset > 0 {
		return nil, 0, nil
	}
	return m.list, len(m.list), nil
}

// fakePrices: a catalog keyed by our id.
type fakePrices map[uuid.UUID]model.CatalogPrice

func (f fakePrices) GetCatalogPrices(_ context.Context, ids []uuid.UUID) ([]model.CatalogPrice, error) {
	var out []model.CatalogPrice
	for _, id := range ids {
		if cp, ok := f[id]; ok {
			out = append(out, cp)
		}
	}
	return out, nil
}

func (f fakePrices) GetCatalogPricesByStripeIDs(_ context.Context, stripeIDs []string) ([]model.CatalogPrice, error) {
	var out []model.CatalogPrice
	for _, cp := range f {
		for _, s := range stripeIDs {
			if cp.StripePriceID == s {
				out = append(out, cp)
			}
		}
	}
	return out, nil
}

// memBilling: companies by Stripe customer, and their billing status.
type memBilling struct {
	customers map[string]uuid.UUID // cus_... -> company
	status    map[uuid.UUID]string
}

func (m *memBilling) GetByStripeCustomerID(_ context.Context, customerID string) (*model.Company, error) {
	id, ok := m.customers[customerID]
	if !ok {
		return nil, model.ErrNotFound
	}
	return &model.Company{ID: id}, nil
}

func (m *memBilling) GetByID(_ context.Context, id uuid.UUID) (*model.Company, error) {
	for cus, cid := range m.customers {
		if cid == id {
			c := cus
			return &model.Company{ID: id, StripeCustomerID: &c}, nil
		}
	}
	return &model.Company{ID: id}, nil
}

func (m *memBilling) SetBillingStatus(_ context.Context, id uuid.UUID, status string) (string, error) {
	old, ok := m.status[id]
	if !ok {
		old = model.BillingNone
	}
	m.status[id] = status
	return old, nil
}

type fakeTrucks int

func (f fakeTrucks) CountActive(context.Context, uuid.UUID) (int, error) { return int(f), nil }

type fakeSubCustomers struct {
	cards   []model.PaymentMethod
	ensured int
}

func (f *fakeSubCustomers) EnsureCustomer(context.Context, auth.Principal, uuid.UUID) (string, error) {
	f.ensured++
	return "cus_A", nil
}

func (f *fakeSubCustomers) ListPaymentMethods(context.Context, auth.Principal, uuid.UUID) ([]model.PaymentMethod, error) {
	return f.cards, nil
}

// fakeSubGateway answers like Stripe: a new subscription is incomplete
// with an open first invoice for sum(amount × quantity).
type fakeSubGateway struct {
	prices        fakePrices
	created       *model.StripeSubscriptionCreate
	current       *model.StripeSubscription // what GetSubscription returns
	calls         []string
	changed       []model.StripeItemChange
	prorationDate int64
	needs3DS      bool // the bank asks for 3-D Secure on the next charge
	scheduled     *model.StripeScheduleChange
	err           error
}

func (f *fakeSubGateway) CreateSubscription(_ context.Context, in model.StripeSubscriptionCreate) (*model.StripeSubscription, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.created = &in
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	snap := &model.StripeSubscription{ID: "sub_1", CustomerID: in.CustomerID, Status: model.SubscriptionIncomplete}
	var due int64
	for i, it := range in.Items {
		snap.Items = append(snap.Items, model.StripeSubscriptionItem{
			ID: "si_" + string(rune('a'+i)), StripePriceID: it.StripePriceID, Quantity: it.Quantity,
			PeriodStart: &start, PeriodEnd: &end,
		})
		for _, cp := range f.prices {
			if cp.StripePriceID == it.StripePriceID {
				due += cp.UnitAmount * int64(it.Quantity)
			}
		}
	}
	snap.LatestInvoice = &model.StripeInvoice{ID: "in_1", Status: "open", AmountDue: due, ClientSecret: "pi_secret"}
	f.current = snap
	return snap, nil
}

// apply changes f.current's items like Stripe would.
func (f *fakeSubGateway) apply(items []model.StripeItemChange) {
	var kept []model.StripeSubscriptionItem
	for _, it := range f.current.Items {
		drop := false
		for _, ch := range items {
			if ch.ID == it.ID {
				if ch.Deleted {
					drop = true
				} else {
					it.Quantity = ch.Quantity
				}
			}
		}
		if !drop {
			kept = append(kept, it)
		}
	}
	for i, ch := range items {
		if ch.ID == "" {
			kept = append(kept, model.StripeSubscriptionItem{ID: fmt.Sprintf("si_new%d", i), StripePriceID: ch.StripePriceID, Quantity: ch.Quantity})
		}
	}
	f.current.Items = kept
}

func (f *fakeSubGateway) PreviewChange(_ context.Context, subID, customerID string, items []model.StripeItemChange) (*model.ProrationPreview, error) {
	f.calls = append(f.calls, "PreviewChange "+subID+" "+customerID)
	f.changed = items
	return &model.ProrationPreview{ProrationDate: 1, Total: 1234, AmountDue: 1234}, f.err
}

func (f *fakeSubGateway) ChangeItems(_ context.Context, subID string, items []model.StripeItemChange, date int64) (*model.StripeSubscription, error) {
	f.calls = append(f.calls, "ChangeItems "+subID)
	if f.err != nil {
		return nil, f.err
	}
	f.changed, f.prorationDate = items, date
	if f.needs3DS {
		// Stripe keeps the old items until the change's invoice is paid.
		f.current.PendingUpdate = true
		f.current.LatestInvoice = &model.StripeInvoice{ID: "in_2", Status: "open", AmountDue: 2500, ClientSecret: "pi_2_secret"}
	} else {
		f.apply(items)
	}
	c := *f.current
	return &c, nil
}

func (f *fakeSubGateway) SetQuantities(_ context.Context, subID string, items []model.StripeItemChange) (*model.StripeSubscription, error) {
	f.calls = append(f.calls, "SetQuantities "+subID)
	if f.err != nil {
		return nil, f.err
	}
	f.changed = items
	f.apply(items)
	c := *f.current
	return &c, nil
}

func (f *fakeSubGateway) SetCancelAtPeriodEnd(_ context.Context, subID string, cancel bool) (*model.StripeSubscription, error) {
	f.calls = append(f.calls, fmt.Sprintf("SetCancelAtPeriodEnd %s %v", subID, cancel))
	f.current.CancelAtPeriodEnd = cancel
	c := *f.current
	return &c, f.err
}

func (f *fakeSubGateway) CancelNow(_ context.Context, subID string) (*model.StripeSubscription, error) {
	f.calls = append(f.calls, "CancelNow "+subID)
	f.current.Status = model.SubscriptionCanceled
	c := *f.current
	return &c, f.err
}

func (f *fakeSubGateway) ScheduleChange(_ context.Context, in model.StripeScheduleChange) (*model.StripeSubscription, error) {
	f.calls = append(f.calls, "ScheduleChange "+in.SubscriptionID+" schedule="+in.ScheduleID)
	if f.err != nil {
		return nil, f.err
	}
	f.scheduled = &in
	at := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	id := in.ScheduleID
	if id == "" {
		id = "sub_sched_1"
	}
	// The current phase keeps the given quantities, like Stripe.
	for i := range f.current.Items {
		for _, ci := range in.CurrentItems {
			if ci.StripePriceID == f.current.Items[i].StripePriceID {
				f.current.Items[i].Quantity = ci.Quantity
			}
		}
	}
	f.current.Schedule = &model.StripeSchedule{ID: id, NextAt: &at, NextItems: in.NextItems}
	c := *f.current
	return &c, nil
}

func (f *fakeSubGateway) ReleaseSchedule(_ context.Context, scheduleID, subID string) (*model.StripeSubscription, error) {
	f.calls = append(f.calls, "ReleaseSchedule "+scheduleID)
	f.current.Schedule = nil
	c := *f.current
	return &c, f.err
}

func (f *fakeSubGateway) GetSubscription(_ context.Context, id string) (*model.StripeSubscription, error) {
	if f.current == nil || f.current.ID != id {
		return nil, model.ErrNotFound
	}
	c := *f.current
	return &c, nil
}

// ---------- setup ----------

type subFixture struct {
	svc       *SubscriptionService
	store     *memSubscriptionStore
	gw        *fakeSubGateway
	customers *fakeSubCustomers
	billing   *memBilling
	audit     *memAuditStore
	perTruck  model.CatalogPrice // $25 / truck / month
	flat      model.CatalogPrice // $100 / month
	yearly    model.CatalogPrice // $250 / truck / year
	archived  model.CatalogPrice
}

func newSubFixture(trucks int) subFixture {
	product := uuid.New()
	support := uuid.New()
	mk := func(stripeID string, productID uuid.UUID, amount int64, interval, unit string, active bool) model.CatalogPrice {
		return model.CatalogPrice{
			Price: model.Price{ID: uuid.New(), ProductID: productID, StripePriceID: stripeID, UnitAmount: amount,
				Interval: interval, BillingUnit: unit, IsActive: active, Currency: "usd"},
			ProductName: "Plan", ProductActive: true,
		}
	}
	f := subFixture{
		perTruck: mk("price_truck", product, 2500, model.IntervalMonth, model.BillingUnitTruck, true),
		flat:     mk("price_flat", support, 10000, model.IntervalMonth, model.BillingUnitCompany, true),
		yearly:   mk("price_year", product, 25000, model.IntervalYear, model.BillingUnitTruck, true),
		archived: mk("price_old", support, 9000, model.IntervalMonth, model.BillingUnitCompany, false),
	}
	prices := fakePrices{f.perTruck.ID: f.perTruck, f.flat.ID: f.flat, f.yearly.ID: f.yearly, f.archived.ID: f.archived}
	f.store = &memSubscriptionStore{subs: map[uuid.UUID]*model.Subscription{}}
	f.gw = &fakeSubGateway{prices: prices}
	f.customers = &fakeSubCustomers{cards: []model.PaymentMethod{
		{ID: "pm_old"}, {ID: "pm_default", IsDefault: true},
	}}
	f.billing = &memBilling{customers: map[string]uuid.UUID{"cus_A": companyA}, status: map[uuid.UUID]string{}}
	var auditor *Auditor
	auditor, f.audit = newTestAuditor()
	f.svc = NewSubscriptionService(f.store, f.billing, prices, fakeTrucks(trucks), f.customers, f.gw, auditor)
	return f
}

func (f subFixture) create(p auth.Principal, prices ...model.CatalogPrice) (*SubscriptionState, error) {
	ids := make([]uuid.UUID, 0, len(prices))
	for _, cp := range prices {
		ids = append(ids, cp.ID)
	}
	return f.svc.Create(context.Background(), p, companyA, CreateSubscriptionInput{PriceIDs: ids})
}

// ---------- tests ----------

func TestSubscription_Create_PayFirst(t *testing.T) {
	f := newSubFixture(12)

	st, err := f.create(companyAUser, f.perTruck, f.flat)

	require.NoError(t, err)
	// Sent to Stripe: per-truck quantity = active trucks, flat = 1.
	require.NotNil(t, f.gw.created)
	assert.Equal(t, []model.StripeSubscriptionItem{
		{StripePriceID: "price_truck", Quantity: 12}, {StripePriceID: "price_flat", Quantity: 1},
	}, f.gw.created.Items)
	assert.Equal(t, "cus_A", f.gw.created.CustomerID)

	sub := st.Subscription
	assert.Equal(t, model.SubscriptionIncomplete, sub.Status, "not usable until paid")
	assert.Equal(t, f.gw.created.ID, sub.ID, "the id sent to Stripe is the id saved here")
	assert.Equal(t, model.IntervalMonth, sub.Interval)
	require.NotNil(t, sub.CurrentPeriodEnd)
	require.Len(t, sub.Items, 2)

	// What the browser needs to pay: 12 × $25 + $100 = $400.
	require.NotNil(t, st.Payment)
	assert.Equal(t, PaymentDue{InvoiceID: "in_1", AmountDue: 40000, ClientSecret: "pi_secret", PaymentMethodID: "pm_default"}, *st.Payment)

	// The company may not use the product yet: pay first.
	assert.Equal(t, model.BillingPending, f.billing.status[companyA])
	assert.Equal(t, []string{"subscription.create", "company.billing_status"}, f.audit.actions())
	e := f.audit.entries[0]
	assert.Equal(t, &companyA, e.CompanyID)
	assert.Equal(t, []string{"Plan × 12", "Plan × 1"}, e.Changes["items"].New)
	f.audit.requireAllInTx(t)
}

func TestSubscription_Create_ThenPaidThenRefresh(t *testing.T) {
	f := newSubFixture(2)
	st, err := f.create(companyAUser, f.perTruck)
	require.NoError(t, err)

	// The browser paid; Stripe now says active and the invoice is paid.
	f.gw.current.Status = model.SubscriptionActive
	f.gw.current.LatestInvoice = &model.StripeInvoice{ID: "in_1", Status: "paid"}

	got, err := f.svc.Refresh(context.Background(), companyAUser, companyA)

	require.NoError(t, err)
	assert.Equal(t, st.Subscription.ID, got.Subscription.ID)
	assert.Equal(t, model.SubscriptionActive, got.Subscription.Status)
	assert.Nil(t, got.Payment, "nothing left to pay")
	assert.Equal(t, model.BillingActive, f.billing.status[companyA], "paid: the company may use the product")
	actions := f.audit.actions()
	assert.Equal(t, []string{"subscription.sync", "company.billing_status"}, actions[len(actions)-2:])
	e := f.audit.entries[len(f.audit.entries)-2]
	assert.Equal(t, model.AuditChange{Old: model.SubscriptionIncomplete, New: model.SubscriptionActive}, e.Changes["status"])

	// Refreshing again changes nothing, so records nothing.
	n := len(f.audit.entries)
	_, err = f.svc.Refresh(context.Background(), companyAUser, companyA)
	require.NoError(t, err)
	assert.Len(t, f.audit.entries, n)
}

func TestSubscription_Refresh_StillUnpaidGivesPaymentAgain(t *testing.T) {
	f := newSubFixture(2)
	_, err := f.create(companyAUser, f.perTruck)
	require.NoError(t, err)

	got, err := f.svc.Refresh(context.Background(), companyAUser, companyA)

	require.NoError(t, err)
	require.NotNil(t, got.Payment, "a closed tab can pay later")
	assert.Equal(t, "pi_secret", got.Payment.ClientSecret)
}

func TestSubscription_Create_ZeroTrucksIsFree(t *testing.T) {
	f := newSubFixture(0)

	st, err := f.create(companyAUser, f.perTruck)

	require.NoError(t, err)
	assert.Equal(t, 0, f.gw.created.Items[0].Quantity)
	assert.Nil(t, st.Payment, "a $0 invoice needs no payment")
}

func TestSubscription_Create_Validation(t *testing.T) {
	f := newSubFixture(5)
	cases := map[string]struct {
		prices []model.CatalogPrice
		field  string
	}{
		"no prices":            {nil, "price_ids"},
		"same price twice":     {[]model.CatalogPrice{f.flat, f.flat}, "price_ids"},
		"archived price":       {[]model.CatalogPrice{f.archived}, "price_ids[0]"},
		"unknown price":        {[]model.CatalogPrice{{Price: model.Price{ID: uuid.New()}}}, "price_ids[0]"},
		"two prices, one plan": {[]model.CatalogPrice{f.perTruck, f.yearly}, "price_ids[1]"},
		"monthly + yearly":     {[]model.CatalogPrice{f.flat, f.yearly}, "price_ids"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.create(companyAUser, tc.prices...)
			requireValidationField(t, err, tc.field)
			assert.Nil(t, f.gw.created, "nothing reaches Stripe")
		})
	}
}

func TestSubscription_Create_ArchivedProduct(t *testing.T) {
	f := newSubFixture(5)
	cp := f.flat
	cp.ProductActive = false
	f.svc.prices = fakePrices{cp.ID: cp}

	_, err := f.create(companyAUser, cp)

	requireValidationField(t, err, "price_ids[0]")
}

func TestSubscription_Create_NeedsDefaultCard(t *testing.T) {
	f := newSubFixture(5)
	f.customers.cards = []model.PaymentMethod{{ID: "pm_x"}} // saved, but not default

	_, err := f.create(companyAUser, f.flat)

	requireValidationField(t, err, "payment_method")
	assert.Nil(t, f.gw.created)
}

func TestSubscription_Create_OnlyOneAtATime(t *testing.T) {
	f := newSubFixture(5)
	_, err := f.create(companyAUser, f.flat)
	require.NoError(t, err)

	_, err = f.create(companyAUser, f.flat)
	assert.ErrorIs(t, err, model.ErrSubscriptionExists)

	// Once the first one has ended, a new one may start.
	for _, s := range f.store.subs {
		s.Status = model.SubscriptionIncompleteExpired
	}
	f.gw.created = nil
	_, err = f.create(companyAUser, f.flat)
	require.NoError(t, err)
}

func TestSubscription_TenantScope(t *testing.T) {
	f := newSubFixture(5)
	other := uuid.New()
	ctx := context.Background()

	_, err := f.svc.Create(ctx, companyAUser, other, CreateSubscriptionInput{PriceIDs: []uuid.UUID{f.flat.ID}})
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = f.svc.Current(ctx, companyAUser, other)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = f.svc.Refresh(ctx, companyAUser, other)
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Zero(t, f.customers.ensured, "no Stripe customer is created for another tenant")

	// The platform acts for any company.
	_, err = f.svc.Create(ctx, platformAdmin, companyA, CreateSubscriptionInput{PriceIDs: []uuid.UUID{f.flat.ID}})
	assert.NoError(t, err)
}

func TestSubscription_Create_StripeFails(t *testing.T) {
	f := newSubFixture(5)
	f.gw.err = errStripeDown

	_, err := f.create(companyAUser, f.flat)

	assert.ErrorIs(t, err, model.ErrPaymentProvider)
	assert.Empty(t, f.store.subs)
	assert.Empty(t, f.audit.entries)
}

func TestSubscription_Sync_UnknownPrice(t *testing.T) {
	f := newSubFixture(5)
	_, err := f.create(companyAUser, f.flat)
	require.NoError(t, err)
	// Someone added a price in the Stripe Dashboard.
	f.gw.current.Items = append(f.gw.current.Items, model.StripeSubscriptionItem{ID: "si_z", StripePriceID: "price_dashboard"})

	_, err = f.svc.Refresh(context.Background(), companyAUser, companyA)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "price_dashboard")
	assert.False(t, errors.Is(err, model.ErrNotFound))
}

func TestSubscription_List_Scope(t *testing.T) {
	f := newSubFixture(5)
	other := uuid.New()

	_, err := f.svc.List(context.Background(), companyAUser, SubscriptionListParams{CompanyID: &other, Status: "active"})
	require.NoError(t, err)
	assert.Equal(t, &companyA, f.store.listF.CompanyID, "company users see only their own")
	assert.Equal(t, "active", f.store.listF.Status)

	_, err = f.svc.List(context.Background(), platformAdmin, SubscriptionListParams{CompanyID: &other})
	require.NoError(t, err)
	assert.Equal(t, &other, f.store.listF.CompanyID)
}

// ---------- webhook sync ----------

func TestSubscription_SyncByStripeID_PaymentFailsThenStripeGivesUp(t *testing.T) {
	f := newSubFixture(3)
	ctx := context.Background()
	_, err := f.create(companyAUser, f.perTruck)
	require.NoError(t, err)
	f.gw.current.Status = model.SubscriptionActive
	_, err = f.svc.SyncByStripeID(ctx, "sub_1")
	require.NoError(t, err)
	require.Equal(t, model.BillingActive, f.billing.status[companyA])

	// A renewal fails: grace period, still allowed.
	f.gw.current.Status = model.SubscriptionPastDue
	sub, err := f.svc.SyncByStripeID(ctx, "sub_1")
	require.NoError(t, err)
	assert.Equal(t, model.SubscriptionPastDue, sub.Status)
	assert.Equal(t, model.BillingPastDue, f.billing.status[companyA])
	assert.True(t, model.BillingAllowsUse(f.billing.status[companyA]))
	last := f.audit.last(t)
	assert.Nil(t, last.ActorID, "webhooks act as the system, not as a user")

	// Stripe's retries are exhausted: blocked.
	f.gw.current.Status = model.SubscriptionUnpaid
	_, err = f.svc.SyncByStripeID(ctx, "sub_1")
	require.NoError(t, err)
	assert.Equal(t, model.BillingBlocked, f.billing.status[companyA])
	assert.False(t, model.BillingAllowsUse(f.billing.status[companyA]))

	// The subscription is canceled: no current subscription any more.
	f.gw.current.Status = model.SubscriptionCanceled
	_, err = f.svc.SyncByStripeID(ctx, "sub_1")
	require.NoError(t, err)
	assert.Equal(t, model.BillingNone, f.billing.status[companyA])
}

func TestSubscription_SyncByStripeID_WebhookBeforeOurSave(t *testing.T) {
	f := newSubFixture(3)
	ourID := uuid.Must(uuid.NewV7())
	start := time.Now()
	f.gw.current = &model.StripeSubscription{
		ID: "sub_early", CustomerID: "cus_A", Status: model.SubscriptionIncomplete,
		Metadata: map[string]string{model.StripeMetaSubscriptionID: ourID.String()},
		Items:    []model.StripeSubscriptionItem{{ID: "si_1", StripePriceID: "price_flat", Quantity: 1, PeriodStart: &start}},
	}

	sub, err := f.svc.SyncByStripeID(context.Background(), "sub_early")

	require.NoError(t, err)
	assert.Equal(t, ourID, sub.ID, "our id comes back from Stripe's metadata")
	assert.Equal(t, companyA, sub.CompanyID, "the company is found by its Stripe customer")
}

func TestSubscription_SyncByStripeID_NotOurs(t *testing.T) {
	f := newSubFixture(3)
	ctx := context.Background()
	f.gw.current = &model.StripeSubscription{ID: "sub_stranger", CustomerID: "cus_unknown", Status: model.SubscriptionActive}

	_, err := f.svc.SyncByStripeID(ctx, "sub_stranger")
	assert.ErrorIs(t, err, ErrNotOurs, "a customer of none of our companies")

	_, err = f.svc.SyncByStripeID(ctx, "sub_deleted_in_stripe")
	assert.ErrorIs(t, err, ErrNotOurs)
	assert.Empty(t, f.store.subs)
}

func TestSubscription_OldEndedSubscriptionDoesNotBlockTheNewOne(t *testing.T) {
	f := newSubFixture(3)
	ctx := context.Background()
	// An old subscription, already ended.
	old := &model.Subscription{ID: uuid.New(), CompanyID: companyA, StripeSubscriptionID: "sub_old", Status: model.SubscriptionCanceled}
	f.store.subs[old.ID] = old
	// The new one, active.
	_, err := f.create(companyAUser, f.flat)
	require.NoError(t, err)
	f.gw.current.Status = model.SubscriptionActive
	_, err = f.svc.SyncByStripeID(ctx, "sub_1")
	require.NoError(t, err)
	require.Equal(t, model.BillingActive, f.billing.status[companyA])

	// A late event about the OLD one arrives.
	newOne := f.gw.current
	f.gw.current = &model.StripeSubscription{ID: "sub_old", CustomerID: "cus_A", Status: model.SubscriptionCanceled}
	_, err = f.svc.SyncByStripeID(ctx, "sub_old")
	require.NoError(t, err)
	f.gw.current = newOne

	assert.Equal(t, model.BillingActive, f.billing.status[companyA], "the status follows the CURRENT subscription")
}
