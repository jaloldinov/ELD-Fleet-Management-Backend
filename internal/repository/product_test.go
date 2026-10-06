package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// createProduct inserts a product with a unique fake Stripe id.
func createProduct(t *testing.T, repo *ProductRepository, name string) *model.Product {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	p, err := repo.CreateProduct(context.Background(), model.Product{
		ID: id, StripeProductID: "prod_" + fakeStripeSuffix(id), Name: name,
	})
	require.NoError(t, err)
	return p
}

func createPrice(t *testing.T, repo *ProductRepository, productID uuid.UUID, amount int64, interval, unit string) *model.Price {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	pr, err := repo.CreatePrice(context.Background(), model.Price{
		ID: id, ProductID: productID, StripePriceID: "price_" + fakeStripeSuffix(id),
		Currency: model.CurrencyUSD, UnitAmount: amount, Interval: interval, BillingUnit: unit,
	})
	require.NoError(t, err)
	return pr
}

// fakeStripeSuffix turns a UUID into letters and digits, like a Stripe id.
func fakeStripeSuffix(id uuid.UUID) string {
	s := id.String()
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] != '-' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func TestProductRepository_CreateAndGet(t *testing.T) {
	repo := NewProductRepository(newTestDB(t))
	ctx := context.Background()

	id := uuid.Must(uuid.NewV7())
	created, err := repo.CreateProduct(ctx, model.Product{
		ID: id, StripeProductID: "prod_ABC123", Name: "ELD Fleet plan", Description: "Hours of service for every truck",
	})
	require.NoError(t, err)
	assert.Equal(t, id, created.ID, "the id chosen by the service is kept")
	assert.True(t, created.IsActive)
	assert.NotNil(t, created.Prices, "[] not null")

	monthly := createPrice(t, repo, id, 2500, model.IntervalMonth, model.BillingUnitTruck)
	yearly := createPrice(t, repo, id, 25000, model.IntervalYear, model.BillingUnitTruck)

	got, err := repo.GetProduct(ctx, id, false)
	require.NoError(t, err)
	assert.Equal(t, "prod_ABC123", got.StripeProductID)
	require.Len(t, got.Prices, 2)
	assert.Equal(t, monthly.ID, got.Prices[0].ID, "monthly first")
	assert.Equal(t, yearly.ID, got.Prices[1].ID)
	assert.Equal(t, int64(2500), got.Prices[0].UnitAmount)
	assert.Equal(t, model.BillingUnitTruck, got.Prices[0].BillingUnit)
	assert.Equal(t, model.CurrencyUSD, got.Prices[0].Currency)

	_, err = repo.GetProduct(ctx, uuid.New(), false)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = repo.GetPrice(ctx, uuid.New())
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestProductRepository_Constraints(t *testing.T) {
	repo := NewProductRepository(newTestDB(t))
	ctx := context.Background()
	p := createProduct(t, repo, "Plan")

	newPrice := func(mut func(*model.Price)) model.Price {
		id := uuid.Must(uuid.NewV7())
		pr := model.Price{ID: id, ProductID: p.ID, StripePriceID: "price_" + fakeStripeSuffix(id),
			Currency: model.CurrencyUSD, UnitAmount: 100, Interval: model.IntervalMonth, BillingUnit: model.BillingUnitCompany}
		mut(&pr)
		return pr
	}
	// The database is the last line of defence when the service has a bug.
	for name, mut := range map[string]func(*model.Price){
		"negative amount": func(pr *model.Price) { pr.UnitAmount = -1 },
		"over Stripe max": func(pr *model.Price) { pr.UnitAmount = 100_000_000 },
		"weekly":          func(pr *model.Price) { pr.Interval = "week" },
		"unknown unit":    func(pr *model.Price) { pr.BillingUnit = "driver" },
		"euro":            func(pr *model.Price) { pr.Currency = "eur" },
		"not a price id":  func(pr *model.Price) { pr.StripePriceID = "prod_123" },
		"no such product": func(pr *model.Price) { pr.ProductID = uuid.New() },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repo.CreatePrice(ctx, newPrice(mut))
			assert.Error(t, err)
		})
	}

	// One Stripe price is one row (matters when webhooks sync, step 6e).
	dup := func(pr *model.Price) { pr.StripePriceID = "price_dup" }
	_, err := repo.CreatePrice(ctx, newPrice(dup))
	require.NoError(t, err)
	_, err = repo.CreatePrice(ctx, newPrice(dup))
	assert.Error(t, err, "same Stripe id twice")

	_, err = repo.CreateProduct(ctx, model.Product{ID: uuid.Must(uuid.NewV7()), StripeProductID: "prod_x", Name: ""})
	assert.Error(t, err, "empty name")
}

func TestProductRepository_ListFiltersAndPrices(t *testing.T) {
	repo := NewProductRepository(newTestDB(t))
	ctx := context.Background()

	active := createProduct(t, repo, "Active")
	archived := createProduct(t, repo, "Archived")
	archived.IsActive = false
	_, err := repo.UpdateProduct(ctx, *archived)
	require.NoError(t, err)
	empty := createProduct(t, repo, "No prices yet")

	live := createPrice(t, repo, active.ID, 2500, model.IntervalMonth, model.BillingUnitTruck)
	old := createPrice(t, repo, active.ID, 2000, model.IntervalMonth, model.BillingUnitTruck)
	old.IsActive = false
	_, err = repo.UpdatePrice(ctx, *old)
	require.NoError(t, err)

	all, total, err := repo.ListProducts(ctx, model.ProductFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	require.Len(t, all, 3)
	assert.Equal(t, empty.ID, all[0].ID, "newest first")
	assert.NotNil(t, all[0].Prices, "products without prices get [] not null")
	assert.Len(t, all[2].Prices, 2, "archived prices included by default")

	yes := true
	onlyActive, total, err := repo.ListProducts(ctx, model.ProductFilter{IsActive: &yes, ActivePricesOnly: true}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	for _, p := range onlyActive {
		assert.NotEqual(t, archived.ID, p.ID)
		if p.ID == active.ID {
			require.Len(t, p.Prices, 1)
			assert.Equal(t, live.ID, p.Prices[0].ID)
		}
	}

	page2, _, err := repo.ListProducts(ctx, model.ProductFilter{}, 2, 2)
	require.NoError(t, err)
	assert.Len(t, page2, 1)
}

func TestProductRepository_Update(t *testing.T) {
	repo := NewProductRepository(newTestDB(t))
	ctx := context.Background()
	p := createProduct(t, repo, "Old name")

	p.Name, p.Description, p.IsActive = "New name", "Now with IFTA", false
	updated, err := repo.UpdateProduct(ctx, *p)
	require.NoError(t, err)
	assert.Equal(t, "New name", updated.Name)
	assert.False(t, updated.IsActive)
	assert.True(t, updated.UpdatedAt.After(updated.CreatedAt), "trigger bumps updated_at")

	pr := createPrice(t, repo, p.ID, 999, model.IntervalYear, model.BillingUnitCompany)
	pr.Nickname, pr.IsActive = "Legacy", false
	pr.UnitAmount = 1 // must be ignored: amounts never change
	got, err := repo.UpdatePrice(ctx, *pr)
	require.NoError(t, err)
	assert.Equal(t, "Legacy", got.Nickname)
	assert.False(t, got.IsActive)
	assert.Equal(t, int64(999), got.UnitAmount)

	_, err = repo.UpdateProduct(ctx, model.Product{ID: uuid.New(), Name: "x"})
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = repo.UpdatePrice(ctx, model.Price{ID: uuid.New()})
	assert.ErrorIs(t, err, model.ErrNotFound)
}
