package handler

import (
	"context"
	"encoding/json"
	"fmt"
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

type fakeCatalogService struct {
	gotCreate      service.CreateProductInput
	gotUpdate      service.UpdateProductInput
	gotList        service.ProductListParams
	gotPrice       service.CreatePriceInput
	gotPriceUpdate service.UpdatePriceInput
	gotID          uuid.UUID
	product        *model.Product
	price          *model.Price
	err            error
}

func (f *fakeCatalogService) CreateProduct(_ context.Context, _ auth.Principal, in service.CreateProductInput) (*model.Product, error) {
	f.gotCreate = in
	return f.product, f.err
}

func (f *fakeCatalogService) GetProduct(_ context.Context, _ auth.Principal, id uuid.UUID) (*model.Product, error) {
	f.gotID = id
	return f.product, f.err
}

func (f *fakeCatalogService) ListProducts(_ context.Context, _ auth.Principal, params service.ProductListParams) (*service.ProductPage, error) {
	f.gotList = params
	if f.err != nil {
		return nil, f.err
	}
	return &service.ProductPage{Items: []model.Product{*f.product}, Total: 1, Page: 1, PageSize: 20}, nil
}

func (f *fakeCatalogService) UpdateProduct(_ context.Context, _ auth.Principal, id uuid.UUID, in service.UpdateProductInput) (*model.Product, error) {
	f.gotID, f.gotUpdate = id, in
	return f.product, f.err
}

func (f *fakeCatalogService) ArchiveProduct(_ context.Context, _ auth.Principal, id uuid.UUID) error {
	f.gotID = id
	return f.err
}

func (f *fakeCatalogService) CreatePrice(_ context.Context, _ auth.Principal, productID uuid.UUID, in service.CreatePriceInput) (*model.Price, error) {
	f.gotID, f.gotPrice = productID, in
	return f.price, f.err
}

func (f *fakeCatalogService) UpdatePrice(_ context.Context, _ auth.Principal, id uuid.UUID, in service.UpdatePriceInput) (*model.Price, error) {
	f.gotID, f.gotPriceUpdate = id, in
	return f.price, f.err
}

var (
	testProductID = uuid.MustParse("0199a1b2-0000-7000-8000-0000000000a1")
	testPriceID   = uuid.MustParse("0199a1b2-0000-7000-8000-0000000000b1")
)

func samplePrice() *model.Price {
	return &model.Price{ID: testPriceID, ProductID: testProductID, StripePriceID: "price_123", Nickname: "Monthly",
		Currency: "usd", UnitAmount: 2500, Interval: model.IntervalMonth, BillingUnit: model.BillingUnitTruck,
		IsActive: true, CreatedAt: time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)}
}

func sampleProduct(prices ...model.Price) *model.Product {
	return &model.Product{ID: testProductID, StripeProductID: "prod_123", Name: "ELD Fleet plan",
		IsActive: true, Prices: prices}
}

func productRouter(svc CatalogService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewProductHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/products")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.PATCH("/:id", h.Update)
	g.DELETE("/:id", h.Archive)
	g.POST("/:id/prices", h.CreatePrice)
	r.PATCH("/api/v1/prices/:id", h.UpdatePrice)
	return r
}

func TestProductHandler_Create(t *testing.T) {
	svc := &fakeCatalogService{product: sampleProduct()}

	rec := do(productRouter(svc), http.MethodPost, "/api/v1/products",
		`{"name":"ELD Fleet plan","description":"HOS"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, service.CreateProductInput{Name: "ELD Fleet plan", Description: "HOS"}, svc.gotCreate)
	assert.Equal(t, "/api/v1/products/"+testProductID.String(), rec.Header().Get("Location"))
	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, []any{}, got["prices"], "[] not null for a product without prices")
	assert.Equal(t, "prod_123", got["stripe_product_id"])
}

func TestProductHandler_ListAndGet(t *testing.T) {
	svc := &fakeCatalogService{product: sampleProduct(*samplePrice())}
	r := productRouter(svc)

	rec := do(r, http.MethodGet, "/api/v1/products?is_active=false&page=2", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, svc.gotList.IsActive)
	assert.False(t, *svc.gotList.IsActive)
	assert.Equal(t, 2, svc.gotList.Page)

	var list ListProductsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)
	require.Len(t, list.Items[0].Prices, 1)
	pr := list.Items[0].Prices[0]
	assert.Equal(t, int64(2500), pr.UnitAmount, "cents, as an integer")
	assert.Equal(t, "month", pr.Interval)
	assert.Equal(t, "truck", pr.BillingUnit)

	assert.Equal(t, http.StatusBadRequest, do(r, http.MethodGet, "/api/v1/products?is_active=maybe", "").Code)

	rec = do(r, http.MethodGet, "/api/v1/products/"+testProductID.String(), "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testProductID, svc.gotID)
}

func TestProductHandler_UpdateAndArchive(t *testing.T) {
	svc := &fakeCatalogService{product: sampleProduct()}
	r := productRouter(svc)

	rec := do(r, http.MethodPatch, "/api/v1/products/"+testProductID.String(), `{"is_active":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, svc.gotUpdate.IsActive)
	assert.True(t, *svc.gotUpdate.IsActive)
	assert.Nil(t, svc.gotUpdate.Name)

	rec = do(r, http.MethodDelete, "/api/v1/products/"+testProductID.String(), "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, testProductID, svc.gotID)
}

func TestProductHandler_CreatePrice(t *testing.T) {
	svc := &fakeCatalogService{price: samplePrice()}

	rec := do(productRouter(svc), http.MethodPost, "/api/v1/products/"+testProductID.String()+"/prices",
		`{"nickname":"Monthly","unit_amount":2500,"interval":"month","billing_unit":"truck"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, testProductID, svc.gotID)
	assert.Equal(t, service.CreatePriceInput{Nickname: "Monthly", UnitAmount: 2500, Interval: "month", BillingUnit: "truck"}, svc.gotPrice)
}

func TestProductHandler_CreatePrice_AmountMustBeCents(t *testing.T) {
	svc := &fakeCatalogService{price: samplePrice()}

	rec := do(productRouter(svc), http.MethodPost, "/api/v1/products/"+testProductID.String()+"/prices",
		`{"unit_amount":25.50,"interval":"month","billing_unit":"truck"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "dollars with decimals are refused: amounts are integer cents")
	assert.Equal(t, "must be an integer", decodeError(t, rec).Details["unit_amount"])
}

func TestProductHandler_UpdatePrice(t *testing.T) {
	svc := &fakeCatalogService{price: samplePrice()}
	r := productRouter(svc)

	rec := do(r, http.MethodPatch, "/api/v1/prices/"+testPriceID.String(), `{"is_active":false,"nickname":"Old"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, testPriceID, svc.gotID)
	assert.Equal(t, "Old", *svc.gotPriceUpdate.Nickname)

	// The amount of a price never changes: the field does not exist here.
	rec = do(r, http.MethodPatch, "/api/v1/prices/"+testPriceID.String(), `{"unit_amount":3000}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "unknown field", decodeError(t, rec).Details["unit_amount"])
}

func TestProductHandler_StripeFailureIs502(t *testing.T) {
	svc := &fakeCatalogService{err: fmt.Errorf("%w: create product: card_declined secret detail", model.ErrPaymentProvider)}

	rec := do(productRouter(svc), http.MethodPost, "/api/v1/products", `{"name":"Plan"}`)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	body := decodeError(t, rec)
	assert.Equal(t, "payment_provider_error", body.Code)
	assert.NotContains(t, body.Message, "secret detail", "Stripe internals are logged, not shown")
}
