package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// activeSubFixture: company A pays monthly for perTruck (3 trucks) + flat.
func activeSubFixture(t *testing.T) subFixture {
	t.Helper()
	f := newSubFixture(3)
	_, err := f.create(companyAUser, f.perTruck, f.flat)
	require.NoError(t, err)
	f.gw.current.Status = model.SubscriptionActive
	f.gw.current.LatestInvoice = &model.StripeInvoice{ID: "in_1", Status: "paid"}
	_, err = f.svc.SyncByStripeID(context.Background(), "sub_1")
	require.NoError(t, err)
	f.gw.calls = nil
	return f
}

func ids(prices ...model.CatalogPrice) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(prices))
	for _, p := range prices {
		out = append(out, p.ID)
	}
	return out
}

func TestDiffItems(t *testing.T) {
	truck := model.SubscriptionItem{StripeSubscriptionItemID: "si_t", Quantity: 3, Price: model.Price{StripePriceID: "price_t"}}
	flat := model.SubscriptionItem{StripeSubscriptionItemID: "si_f", Quantity: 1, Price: model.Price{StripePriceID: "price_f"}}
	current := []model.SubscriptionItem{truck, flat}

	cases := map[string]struct {
		desired []model.StripeSubscriptionItem
		want    []model.StripeItemChange
	}{
		"same plan: nothing": {
			[]model.StripeSubscriptionItem{{StripePriceID: "price_t", Quantity: 3}, {StripePriceID: "price_f", Quantity: 1}}, nil},
		"more trucks: quantity": {
			[]model.StripeSubscriptionItem{{StripePriceID: "price_t", Quantity: 5}, {StripePriceID: "price_f", Quantity: 1}},
			[]model.StripeItemChange{{ID: "si_t", Quantity: 5}}},
		"drop support": {
			[]model.StripeSubscriptionItem{{StripePriceID: "price_t", Quantity: 3}},
			[]model.StripeItemChange{{ID: "si_f", Deleted: true}}},
		"switch price (monthly to yearly): remove + add": {
			[]model.StripeSubscriptionItem{{StripePriceID: "price_t_year", Quantity: 3}},
			[]model.StripeItemChange{{ID: "si_t", Deleted: true}, {ID: "si_f", Deleted: true}, {StripePriceID: "price_t_year", Quantity: 3}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, diffItems(current, tc.desired))
		})
	}
}

func TestChange_Preview(t *testing.T) {
	f := activeSubFixture(t)

	got, err := f.svc.PreviewChange(context.Background(), companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})

	require.NoError(t, err)
	assert.Equal(t, int64(1234), got.AmountDue)
	assert.Equal(t, []string{"PreviewChange sub_1 cus_A"}, f.gw.calls, "a preview changes nothing")
	assert.Equal(t, []model.StripeItemChange{{ID: "si_b", Deleted: true}}, f.gw.changed, "drop the flat item")
}

func TestChange_Applied(t *testing.T) {
	f := activeSubFixture(t)
	date := time.Now().Add(-time.Minute).Unix() // from the preview the user saw

	st, err := f.svc.Change(context.Background(), companyAUser, companyA, ChangeSubscriptionInput{
		PriceIDs: ids(f.perTruck), ProrationDate: &date,
	})

	require.NoError(t, err)
	assert.Equal(t, date, f.gw.prorationDate, "charged exactly what was previewed")
	require.Len(t, st.Subscription.Items, 1)
	assert.Nil(t, st.Payment, "paid off-session with the default card")
	e := f.audit.last(t)
	assert.Equal(t, "subscription.sync", e.Action)
	assert.Equal(t, []string{"Plan × 3"}, e.Changes["items"].New)
}

func TestChange_WaitsFor3DS(t *testing.T) {
	f := activeSubFixture(t)
	f.gw.needs3DS = true

	st, err := f.svc.Change(context.Background(), companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})

	require.NoError(t, err)
	assert.Len(t, st.Subscription.Items, 2, "the old plan stays until the change is paid")
	require.NotNil(t, st.Payment)
	assert.Equal(t, "pi_2_secret", st.Payment.ClientSecret)
	assert.Equal(t, "pm_default", st.Payment.PaymentMethodID)
}

func TestChange_Rules(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()
	future := time.Now().Add(time.Minute).Unix()
	stale := time.Now().Add(-2 * time.Hour).Unix()

	_, err := f.svc.Change(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck, f.flat)})
	requireValidationField(t, err, "price_ids") // already the current plan
	_, err = f.svc.Change(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck), ProrationDate: &stale})
	requireValidationField(t, err, "proration_date")
	_, err = f.svc.Change(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck), ProrationDate: &future})
	requireValidationField(t, err, "proration_date")
	_, err = f.svc.Change(ctx, companyAUser, uuid.New(), ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})
	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Empty(t, f.gw.calls, "nothing reached Stripe")

	for _, status := range []string{model.SubscriptionIncomplete, model.SubscriptionPastDue} {
		for _, s := range f.store.subs {
			s.Status = status
		}
		_, err = f.svc.Change(ctx, companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.perTruck)})
		requireValidationField(t, err, "subscription")
	}
}

func TestChange_SwitchToYearly(t *testing.T) {
	f := activeSubFixture(t)

	_, err := f.svc.Change(context.Background(), companyAUser, companyA, ChangeSubscriptionInput{PriceIDs: ids(f.yearly)})

	require.NoError(t, err)
	assert.Equal(t, []model.StripeItemChange{
		{ID: "si_a", Deleted: true}, {ID: "si_b", Deleted: true}, {StripePriceID: "price_year", Quantity: 3},
	}, f.gw.changed)
}

func TestCancel(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()

	sub, err := f.svc.Cancel(ctx, companyAUser, companyA, true)
	require.NoError(t, err)
	assert.True(t, sub.CancelAtPeriodEnd)
	assert.Equal(t, model.SubscriptionActive, sub.Status, "service continues until the period ends")
	assert.Equal(t, model.BillingActive, f.billing.status[companyA])

	// Twice: nothing new.
	f.gw.calls = nil
	_, err = f.svc.Cancel(ctx, companyAUser, companyA, true)
	require.NoError(t, err)
	assert.Empty(t, f.gw.calls)

	// Changed their mind.
	sub, err = f.svc.Resume(ctx, companyAUser, companyA)
	require.NoError(t, err)
	assert.False(t, sub.CancelAtPeriodEnd)
	_, err = f.svc.Resume(ctx, companyAUser, companyA)
	requireValidationField(t, err, "subscription")
}

func TestCancel_NowIsPlatformOnly(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()

	_, err := f.svc.Cancel(ctx, companyAUser, companyA, false)
	assert.ErrorIs(t, err, model.ErrForbidden)

	sub, err := f.svc.Cancel(ctx, platformAdmin, companyA, false)
	require.NoError(t, err)
	assert.Equal(t, model.SubscriptionCanceled, sub.Status)
	assert.Equal(t, model.BillingNone, f.billing.status[companyA], "ended: the fleet is locked")
}

func TestSyncTruckQuantity(t *testing.T) {
	f := activeSubFixture(t)
	ctx := context.Background()

	// Same count: no Stripe call.
	changed, err := f.svc.SyncTruckQuantity(ctx, companyA)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, f.gw.calls)

	// Two trucks added: only the per-truck item changes.
	f.svc.trucks = fakeTrucks(5)
	changed, err = f.svc.SyncTruckQuantity(ctx, companyA)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []model.StripeItemChange{{ID: "si_a", Quantity: 5}}, f.gw.changed)
	assert.Nil(t, f.audit.last(t).ActorID, "a fleet change syncs as the system")

	current, err := f.svc.Current(ctx, companyAUser, companyA)
	require.NoError(t, err)
	for _, it := range current.Items {
		if it.Price.BillingUnit == model.BillingUnitTruck {
			assert.Equal(t, 5, it.Quantity)
		}
	}
}

func TestSyncTruckQuantity_SkipsWhatIsNotRunning(t *testing.T) {
	f := newSubFixture(3)
	ctx := context.Background()

	changed, err := f.svc.SyncTruckQuantity(ctx, companyA)
	require.NoError(t, err)
	assert.False(t, changed, "no subscription")

	_, err = f.create(companyAUser, f.perTruck) // incomplete: first invoice unpaid
	require.NoError(t, err)
	f.gw.calls = nil
	f.svc.trucks = fakeTrucks(9)
	changed, err = f.svc.SyncTruckQuantity(ctx, companyA)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, f.gw.calls)
}

func TestReconcileTruckQuantities(t *testing.T) {
	f := activeSubFixture(t)
	f.svc.trucks = fakeTrucks(7)
	// The memory store's List ignores filters; give it what SQL would return.
	f.store.list = []model.Subscription{*f.store.subs[firstID(f.store)]}

	updated, err := f.svc.ReconcileTruckQuantities(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, updated, "found once (active), not again under trialing/past_due")
}

func firstID(m *memSubscriptionStore) uuid.UUID {
	for id := range m.subs {
		return id
	}
	return uuid.Nil
}
