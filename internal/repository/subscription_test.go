package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// subscriptionFixture: a company and a product with a monthly per-truck
// price and a monthly flat price.
type subscriptionFixture struct {
	repo     *SubscriptionRepository
	products *ProductRepository
	company  *model.Company
	perTruck *model.Price
	flat     *model.Price
}

func newSubscriptionFixture(t *testing.T) subscriptionFixture {
	t.Helper()
	db := newTestDB(t)
	products := NewProductRepository(db)
	p := createProduct(t, products, "ELD Fleet plan")
	return subscriptionFixture{
		repo:     NewSubscriptionRepository(db),
		products: products,
		company:  createCompany(t, NewCompanyRepository(db), "Acme", "1"),
		perTruck: createPrice(t, products, p.ID, 2500, model.IntervalMonth, model.BillingUnitTruck),
		flat:     createPrice(t, products, p.ID, 10000, model.IntervalMonth, model.BillingUnitCompany),
	}
}

func (f subscriptionFixture) sub(stripeID, status string, items ...model.SubscriptionItem) model.Subscription {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	return model.Subscription{
		ID: uuid.Must(uuid.NewV7()), CompanyID: f.company.ID, StripeSubscriptionID: stripeID,
		Status: status, Interval: model.IntervalMonth, CurrentPeriodStart: &start, CurrentPeriodEnd: &end,
		Items: items,
	}
}

func item(stripeID string, price *model.Price, qty int) model.SubscriptionItem {
	return model.SubscriptionItem{StripeSubscriptionItemID: stripeID, Price: *price, Quantity: qty}
}

func TestSubscriptionRepository_UpsertCreatesThenUpdates(t *testing.T) {
	f := newSubscriptionFixture(t)
	ctx := context.Background()

	in := f.sub("sub_1", model.SubscriptionIncomplete, item("si_a", f.perTruck, 12), item("si_b", f.flat, 1))
	created, err := f.repo.Upsert(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, in.ID, created.ID, "a new subscription keeps the id the service chose")
	assert.Equal(t, model.SubscriptionIncomplete, created.Status)
	require.Len(t, created.Items, 2)
	byStripe := map[string]model.SubscriptionItem{}
	for _, it := range created.Items {
		byStripe[it.StripeSubscriptionItemID] = it
	}
	assert.Equal(t, 12, byStripe["si_a"].Quantity)
	assert.Equal(t, int64(2500), byStripe["si_a"].Price.UnitAmount, "items come back with their price")
	assert.Equal(t, "ELD Fleet plan", byStripe["si_a"].ProductName)

	// The same Stripe subscription again (paid, one item dropped): updated
	// in place, items replaced, our id kept.
	again := f.sub("sub_1", model.SubscriptionActive, item("si_a", f.perTruck, 14))
	again.ID = uuid.Must(uuid.NewV7()) // ignored: the row exists already
	updated, err := f.repo.Upsert(ctx, again)
	require.NoError(t, err)
	assert.Equal(t, created.ID, updated.ID)
	assert.Equal(t, model.SubscriptionActive, updated.Status)
	require.Len(t, updated.Items, 1)
	assert.Equal(t, 14, updated.Items[0].Quantity)
}

func TestSubscriptionRepository_OneLivePerCompany(t *testing.T) {
	f := newSubscriptionFixture(t)
	ctx := context.Background()

	_, err := f.repo.Upsert(ctx, f.sub("sub_1", model.SubscriptionIncomplete, item("si_1", f.flat, 1)))
	require.NoError(t, err)

	_, err = f.repo.Upsert(ctx, f.sub("sub_2", model.SubscriptionIncomplete, item("si_2", f.flat, 1)))
	assert.ErrorIs(t, err, model.ErrSubscriptionExists)

	// Once the first one has ended, a new one may start.
	_, err = f.repo.Upsert(ctx, f.sub("sub_1", model.SubscriptionIncompleteExpired, item("si_1", f.flat, 1)))
	require.NoError(t, err)
	second, err := f.repo.Upsert(ctx, f.sub("sub_2", model.SubscriptionIncomplete, item("si_2", f.flat, 1)))
	require.NoError(t, err)

	current, err := f.repo.GetCurrent(ctx, f.company.ID)
	require.NoError(t, err)
	assert.Equal(t, second.ID, current.ID, "the ended one is history, not current")
}

func TestSubscriptionRepository_GetCurrentNone(t *testing.T) {
	f := newSubscriptionFixture(t)

	_, err := f.repo.GetCurrent(context.Background(), f.company.ID)

	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestSubscriptionRepository_FailedUpsertLeavesNothing(t *testing.T) {
	f := newSubscriptionFixture(t)
	ctx := context.Background()

	// The second item points to a price that does not exist: the whole
	// upsert is rolled back, the subscription row included.
	bad := f.sub("sub_1", model.SubscriptionIncomplete,
		item("si_ok", f.flat, 1), item("si_bad", &model.Price{ID: uuid.New()}, 1))
	_, err := f.repo.Upsert(ctx, bad)
	require.Error(t, err)

	_, err = f.repo.GetCurrent(ctx, f.company.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestSubscriptionRepository_List(t *testing.T) {
	f := newSubscriptionFixture(t)
	ctx := context.Background()
	other := createCompany(t, NewCompanyRepository(f.repo.db), "Other", "2")

	_, err := f.repo.Upsert(ctx, f.sub("sub_old", model.SubscriptionCanceled, item("si_1", f.flat, 1)))
	require.NoError(t, err)
	_, err = f.repo.Upsert(ctx, f.sub("sub_new", model.SubscriptionActive, item("si_2", f.perTruck, 3)))
	require.NoError(t, err)
	theirs := f.sub("sub_other", model.SubscriptionActive, item("si_3", f.flat, 1))
	theirs.CompanyID = other.ID
	_, err = f.repo.Upsert(ctx, theirs)
	require.NoError(t, err)

	all, total, err := f.repo.List(ctx, model.SubscriptionFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Equal(t, "sub_other", all[0].StripeSubscriptionID, "newest first")
	assert.Len(t, all[1].Items, 1)

	mine, total, err := f.repo.List(ctx, model.SubscriptionFilter{CompanyID: &f.company.ID}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, mine, 2)

	active, total, err := f.repo.List(ctx, model.SubscriptionFilter{Status: model.SubscriptionActive}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, active, 2)
}

func TestProductRepository_CatalogPrices(t *testing.T) {
	f := newSubscriptionFixture(t)
	ctx := context.Background()

	got, err := f.products.GetCatalogPrices(ctx, []uuid.UUID{f.perTruck.ID, uuid.New()})
	require.NoError(t, err)
	require.Len(t, got, 1, "unknown ids are left out")
	assert.Equal(t, "ELD Fleet plan", got[0].ProductName)
	assert.True(t, got[0].ProductActive)

	byStripe, err := f.products.GetCatalogPricesByStripeIDs(ctx, []string{f.flat.StripePriceID})
	require.NoError(t, err)
	require.Len(t, byStripe, 1)
	assert.Equal(t, f.flat.ID, byStripe[0].ID)
}

func TestTruckRepository_CountActive(t *testing.T) {
	db := newTestDB(t)
	c := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	repo := NewTruckRepository(db)
	ctx := context.Background()

	_, err := repo.Create(ctx, newTruck(c.ID, "101", vin1))
	require.NoError(t, err)
	parked, err := repo.Create(ctx, newTruck(c.ID, "102", vin2))
	require.NoError(t, err)
	parked.IsActive = false
	_, err = repo.Update(ctx, *parked)
	require.NoError(t, err)
	gone, err := repo.Create(ctx, newTruck(c.ID, "103", vin3))
	require.NoError(t, err)
	require.NoError(t, repo.SoftDelete(ctx, gone.ID))

	n, err := repo.CountActive(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "inactive and deleted trucks are not billed")
}
