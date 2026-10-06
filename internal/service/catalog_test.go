package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// ---------- fakes ----------

type memProductRepo struct {
	products map[uuid.UUID]*model.Product
	prices   map[uuid.UUID]*model.Price
	listF    model.ProductFilter
	failSave error // returned by every write, to simulate a database failure
}

func newMemProductRepo() *memProductRepo {
	return &memProductRepo{products: map[uuid.UUID]*model.Product{}, prices: map[uuid.UUID]*model.Price{}}
}

func (m *memProductRepo) addProduct(active bool) *model.Product {
	p := &model.Product{ID: uuid.New(), StripeProductID: "prod_seed", Name: "Seed", IsActive: active}
	m.products[p.ID] = p
	return p
}

func (m *memProductRepo) addPrice(productID uuid.UUID) *model.Price {
	pr := &model.Price{ID: uuid.New(), ProductID: productID, StripePriceID: "price_seed", Currency: "usd",
		UnitAmount: 2500, Interval: model.IntervalMonth, BillingUnit: model.BillingUnitTruck, IsActive: true}
	m.prices[pr.ID] = pr
	return pr
}

func (m *memProductRepo) CreateProduct(_ context.Context, p model.Product) (*model.Product, error) {
	if m.failSave != nil {
		return nil, m.failSave
	}
	m.products[p.ID] = &p
	c := p
	return &c, nil
}

func (m *memProductRepo) GetProduct(_ context.Context, id uuid.UUID, _ bool) (*model.Product, error) {
	p, ok := m.products[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *p
	return &c, nil
}

func (m *memProductRepo) ListProducts(_ context.Context, f model.ProductFilter, _, _ int) ([]model.Product, int, error) {
	m.listF = f
	return nil, 0, nil
}

func (m *memProductRepo) UpdateProduct(_ context.Context, p model.Product) (*model.Product, error) {
	if m.failSave != nil {
		return nil, m.failSave
	}
	m.products[p.ID] = &p
	c := p
	return &c, nil
}

func (m *memProductRepo) CreatePrice(_ context.Context, pr model.Price) (*model.Price, error) {
	if m.failSave != nil {
		return nil, m.failSave
	}
	m.prices[pr.ID] = &pr
	c := pr
	return &c, nil
}

func (m *memProductRepo) GetPrice(_ context.Context, id uuid.UUID) (*model.Price, error) {
	pr, ok := m.prices[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *pr
	return &c, nil
}

func (m *memProductRepo) UpdatePrice(_ context.Context, pr model.Price) (*model.Price, error) {
	if m.failSave != nil {
		return nil, m.failSave
	}
	m.prices[pr.ID] = &pr
	c := pr
	return &c, nil
}

// fakeCatalogGateway records what would have been sent to Stripe.
type fakeCatalogGateway struct {
	calls          []string
	createdProduct model.Product
	createdPrice   model.Price
	priceProductID string
	updatedProduct model.Product
	updatedPrice   model.Price
	err            error
}

func (f *fakeCatalogGateway) CreateProduct(_ context.Context, p model.Product) (string, error) {
	f.calls = append(f.calls, "CreateProduct")
	f.createdProduct = p
	return "prod_new", f.err
}

func (f *fakeCatalogGateway) UpdateProduct(_ context.Context, p model.Product) error {
	f.calls = append(f.calls, "UpdateProduct")
	f.updatedProduct = p
	return f.err
}

func (f *fakeCatalogGateway) CreatePrice(_ context.Context, stripeProductID string, pr model.Price) (string, error) {
	f.calls = append(f.calls, "CreatePrice")
	f.priceProductID, f.createdPrice = stripeProductID, pr
	return "price_new", f.err
}

func (f *fakeCatalogGateway) UpdatePrice(_ context.Context, pr model.Price) error {
	f.calls = append(f.calls, "UpdatePrice")
	f.updatedPrice = pr
	return f.err
}

func newCatalogSvc() (*CatalogService, *memProductRepo, *fakeCatalogGateway, *memAuditStore) {
	repo := newMemProductRepo()
	gw := &fakeCatalogGateway{}
	audit, store := newTestAuditor()
	return NewCatalogService(repo, gw, audit), repo, gw, store
}

var errStripeDown = fmt.Errorf("%w: stripe is down", model.ErrPaymentProvider)

// ---------- products ----------

func TestCatalog_CreateProduct(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()

	got, err := svc.CreateProduct(context.Background(), platformAdmin, CreateProductInput{
		Name: "  ELD Fleet plan ", Description: " HOS for every truck ",
	})

	require.NoError(t, err)
	assert.Equal(t, "ELD Fleet plan", got.Name, "trimmed")
	assert.Equal(t, "prod_new", got.StripeProductID)
	assert.True(t, got.IsActive)
	assert.Equal(t, gw.createdProduct.ID, got.ID, "the id sent to Stripe is the id saved here")
	assert.Equal(t, byte(7), got.ID[6]>>4, "UUIDv7, like ids made by the database")
	assert.Contains(t, repo.products, got.ID)

	e := store.last(t)
	assert.Equal(t, "product.create", e.Action)
	assert.Equal(t, model.AuditProduct, e.Entity)
	assert.Nil(t, e.CompanyID, "the catalog is platform-level")
	assert.Equal(t, "prod_new", e.Changes["stripe_product_id"].New)
	store.requireAllInTx(t)
}

func TestCatalog_CreateProduct_PlatformOnly(t *testing.T) {
	svc, _, gw, _ := newCatalogSvc()

	_, err := svc.CreateProduct(context.Background(), companyAUser, CreateProductInput{Name: "Mine"})

	assert.ErrorIs(t, err, model.ErrForbidden)
	assert.Empty(t, gw.calls, "nothing reaches Stripe")
}

func TestCatalog_CreateProduct_Validation(t *testing.T) {
	svc, _, gw, _ := newCatalogSvc()
	long := make([]byte, 1001)
	for i := range long {
		long[i] = 'x'
	}

	_, err := svc.CreateProduct(context.Background(), platformAdmin, CreateProductInput{Name: "  ", Description: string(long)})

	requireValidationField(t, err, "name")
	requireValidationField(t, err, "description")
	assert.Empty(t, gw.calls, "invalid input never reaches Stripe")
}

func TestCatalog_CreateProduct_StripeFails(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()
	gw.err = errStripeDown

	_, err := svc.CreateProduct(context.Background(), platformAdmin, CreateProductInput{Name: "Plan"})

	assert.ErrorIs(t, err, model.ErrPaymentProvider)
	assert.Empty(t, repo.products, "nothing saved when Stripe fails")
	assert.Empty(t, store.entries)
}

func TestCatalog_CreateProduct_SaveFailsAfterStripe(t *testing.T) {
	svc, repo, _, _ := newCatalogSvc()
	repo.failSave = errors.New("database is down")

	_, err := svc.CreateProduct(context.Background(), platformAdmin, CreateProductInput{Name: "Plan"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "prod_new", "the error names the orphan in Stripe, for repair")
}

func TestCatalog_GetProduct_CompanyUsersSeeOnlyActive(t *testing.T) {
	svc, repo, _, _ := newCatalogSvc()
	archived := repo.addProduct(false)
	active := repo.addProduct(true)

	_, err := svc.GetProduct(context.Background(), companyAUser, archived.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)

	_, err = svc.GetProduct(context.Background(), companyAUser, active.ID)
	assert.NoError(t, err)
	_, err = svc.GetProduct(context.Background(), platformAdmin, archived.ID)
	assert.NoError(t, err, "the platform sees archived products")
}

func TestCatalog_ListProducts_Scope(t *testing.T) {
	svc, repo, _, _ := newCatalogSvc()
	no := false

	_, err := svc.ListProducts(context.Background(), companyAUser, ProductListParams{IsActive: &no})
	require.NoError(t, err)
	require.NotNil(t, repo.listF.IsActive)
	assert.True(t, *repo.listF.IsActive, "company users cannot ask for archived products")
	assert.True(t, repo.listF.ActivePricesOnly)

	_, err = svc.ListProducts(context.Background(), platformAdmin, ProductListParams{IsActive: &no})
	require.NoError(t, err)
	assert.False(t, *repo.listF.IsActive)
	assert.False(t, repo.listF.ActivePricesOnly)
}

func TestCatalog_UpdateProduct(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()
	p := repo.addProduct(true)

	got, err := svc.UpdateProduct(context.Background(), platformAdmin, p.ID, UpdateProductInput{Name: ptr(" Pro plan ")})

	require.NoError(t, err)
	assert.Equal(t, "Pro plan", got.Name)
	assert.Equal(t, []string{"UpdateProduct"}, gw.calls)
	assert.Equal(t, "prod_seed", gw.updatedProduct.StripeProductID)
	e := store.last(t)
	assert.Equal(t, "product.update", e.Action)
	assert.Equal(t, model.AuditChange{Old: "Seed", New: "Pro plan"}, e.Changes["name"])
	assert.Len(t, e.Changes, 1)
}

func TestCatalog_UpdateProduct_NoChangeNoCall(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()
	p := repo.addProduct(true)

	_, err := svc.UpdateProduct(context.Background(), platformAdmin, p.ID, UpdateProductInput{Name: ptr("Seed")})

	require.NoError(t, err)
	assert.Empty(t, gw.calls)
	assert.Empty(t, store.entries)
}

func TestCatalog_UpdateProduct_Rules(t *testing.T) {
	svc, repo, _, _ := newCatalogSvc()
	p := repo.addProduct(true)
	ctx := context.Background()

	_, err := svc.UpdateProduct(ctx, platformAdmin, p.ID, UpdateProductInput{})
	requireValidationField(t, err, "body")
	_, err = svc.UpdateProduct(ctx, platformAdmin, p.ID, UpdateProductInput{Name: ptr("")})
	requireValidationField(t, err, "name")
	_, err = svc.UpdateProduct(ctx, companyAUser, p.ID, UpdateProductInput{Name: ptr("x")})
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = svc.UpdateProduct(ctx, platformAdmin, uuid.New(), UpdateProductInput{Name: ptr("x")})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestCatalog_ArchiveProduct(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()
	p := repo.addProduct(true)
	ctx := context.Background()

	require.NoError(t, svc.ArchiveProduct(ctx, platformAdmin, p.ID))
	assert.False(t, repo.products[p.ID].IsActive)
	assert.False(t, gw.updatedProduct.IsActive, "archived in Stripe too")
	assert.Equal(t, "product.archive", store.last(t).Action)

	// Again: succeeds, but nothing to do.
	gw.calls = nil
	require.NoError(t, svc.ArchiveProduct(ctx, platformAdmin, p.ID))
	assert.Empty(t, gw.calls)

	assert.ErrorIs(t, svc.ArchiveProduct(ctx, companyAUser, p.ID), model.ErrForbidden)
}

// ---------- prices ----------

func validPriceInput() CreatePriceInput {
	return CreatePriceInput{Nickname: " Monthly ", UnitAmount: 2500, Interval: model.IntervalMonth, BillingUnit: model.BillingUnitTruck}
}

func TestCatalog_CreatePrice(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()
	p := repo.addProduct(true)

	got, err := svc.CreatePrice(context.Background(), platformAdmin, p.ID, validPriceInput())

	require.NoError(t, err)
	assert.Equal(t, "price_new", got.StripePriceID)
	assert.Equal(t, "Monthly", got.Nickname)
	assert.Equal(t, model.CurrencyUSD, got.Currency)
	assert.Equal(t, p.ID, got.ProductID)
	assert.Equal(t, "prod_seed", gw.priceProductID, "the price is attached to the product in Stripe")
	assert.Equal(t, got.ID, gw.createdPrice.ID)
	e := store.last(t)
	assert.Equal(t, "price.create", e.Action)
	assert.Equal(t, model.AuditPrice, e.Entity)
	assert.Equal(t, int64(2500), e.Changes["unit_amount"].New)
	store.requireAllInTx(t)
}

func TestCatalog_CreatePrice_Validation(t *testing.T) {
	cases := map[string]struct {
		mut   func(*CreatePriceInput)
		field string
	}{
		"negative amount": {func(in *CreatePriceInput) { in.UnitAmount = -1 }, "unit_amount"},
		"over the max":    {func(in *CreatePriceInput) { in.UnitAmount = 100_000_000 }, "unit_amount"},
		"weekly":          {func(in *CreatePriceInput) { in.Interval = "week" }, "interval"},
		"missing unit":    {func(in *CreatePriceInput) { in.BillingUnit = "" }, "billing_unit"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, repo, gw, _ := newCatalogSvc()
			p := repo.addProduct(true)
			in := validPriceInput()
			tc.mut(&in)

			_, err := svc.CreatePrice(context.Background(), platformAdmin, p.ID, in)

			requireValidationField(t, err, tc.field)
			assert.Empty(t, gw.calls)
		})
	}
}

func TestCatalog_CreatePrice_FreeIsAllowed(t *testing.T) {
	svc, repo, _, _ := newCatalogSvc()
	in := validPriceInput()
	in.UnitAmount = 0

	_, err := svc.CreatePrice(context.Background(), platformAdmin, repo.addProduct(true).ID, in)

	assert.NoError(t, err, "a free plan is a valid price")
}

func TestCatalog_CreatePrice_ArchivedProduct(t *testing.T) {
	svc, repo, gw, _ := newCatalogSvc()

	_, err := svc.CreatePrice(context.Background(), platformAdmin, repo.addProduct(false).ID, validPriceInput())

	requireValidationField(t, err, "product")
	assert.Empty(t, gw.calls)
}

func TestCatalog_CreatePrice_Rules(t *testing.T) {
	svc, repo, _, _ := newCatalogSvc()
	ctx := context.Background()

	_, err := svc.CreatePrice(ctx, companyAUser, repo.addProduct(true).ID, validPriceInput())
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = svc.CreatePrice(ctx, platformAdmin, uuid.New(), validPriceInput())
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestCatalog_UpdatePrice(t *testing.T) {
	svc, repo, gw, store := newCatalogSvc()
	pr := repo.addPrice(repo.addProduct(true).ID)

	got, err := svc.UpdatePrice(context.Background(), platformAdmin, pr.ID, UpdatePriceInput{
		Nickname: ptr("Legacy"), IsActive: ptr(false),
	})

	require.NoError(t, err)
	assert.Equal(t, "Legacy", got.Nickname)
	assert.False(t, got.IsActive)
	assert.Equal(t, int64(2500), got.UnitAmount, "amount untouched")
	assert.Equal(t, "price_seed", gw.updatedPrice.StripePriceID)
	e := store.last(t)
	assert.Equal(t, "price.update", e.Action)
	assert.Len(t, e.Changes, 2)
}

func TestCatalog_UpdatePrice_Rules(t *testing.T) {
	svc, repo, gw, _ := newCatalogSvc()
	pr := repo.addPrice(repo.addProduct(true).ID)
	ctx := context.Background()

	_, err := svc.UpdatePrice(ctx, platformAdmin, pr.ID, UpdatePriceInput{})
	requireValidationField(t, err, "body")
	_, err = svc.UpdatePrice(ctx, companyAUser, pr.ID, UpdatePriceInput{IsActive: ptr(false)})
	assert.ErrorIs(t, err, model.ErrForbidden)
	_, err = svc.UpdatePrice(ctx, platformAdmin, uuid.New(), UpdatePriceInput{IsActive: ptr(false)})
	assert.ErrorIs(t, err, model.ErrNotFound)

	gw.err = errStripeDown
	_, err = svc.UpdatePrice(ctx, platformAdmin, pr.ID, UpdatePriceInput{IsActive: ptr(false)})
	assert.ErrorIs(t, err, model.ErrPaymentProvider)
	assert.True(t, repo.prices[pr.ID].IsActive, "not saved when Stripe fails")
}
