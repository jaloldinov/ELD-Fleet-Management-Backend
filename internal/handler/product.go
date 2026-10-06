package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// CatalogService is what ProductHandler needs from the service layer.
type CatalogService interface {
	CreateProduct(ctx context.Context, p auth.Principal, in service.CreateProductInput) (*model.Product, error)
	GetProduct(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Product, error)
	ListProducts(ctx context.Context, p auth.Principal, params service.ProductListParams) (*service.ProductPage, error)
	UpdateProduct(ctx context.Context, p auth.Principal, id uuid.UUID, in service.UpdateProductInput) (*model.Product, error)
	ArchiveProduct(ctx context.Context, p auth.Principal, id uuid.UUID) error
	CreatePrice(ctx context.Context, p auth.Principal, productID uuid.UUID, in service.CreatePriceInput) (*model.Price, error)
	UpdatePrice(ctx context.Context, p auth.Principal, id uuid.UUID, in service.UpdatePriceInput) (*model.Price, error)
}

// ---------- DTOs ----------

type CreateProductRequest struct {
	Name        string `json:"name" example:"ELD Fleet plan"`
	Description string `json:"description,omitempty" example:"Hours of service and DVIR for every truck"`
}

type UpdateProductRequest struct {
	Name        *string `json:"name,omitempty" example:"ELD Fleet plan"`
	Description *string `json:"description,omitempty" example:"Now with IFTA reports"`
	// false archives, true restores.
	IsActive *bool `json:"is_active,omitempty" example:"true"`
}

type CreatePriceRequest struct {
	Nickname string `json:"nickname,omitempty" example:"Monthly"`
	// Amount in cents: 2500 = $25.00. 0 to 99999999. Never changes later.
	UnitAmount int64 `json:"unit_amount" example:"2500"`
	// month or year. Never changes later.
	Interval string `json:"interval" example:"month" enums:"month,year"`
	// company = flat amount per company; truck = per active truck. Never changes later.
	BillingUnit string `json:"billing_unit" example:"truck" enums:"company,truck"`
}

// UpdatePriceRequest: amount, interval and unit cannot change; create a new
// price and archive this one instead.
type UpdatePriceRequest struct {
	Nickname *string `json:"nickname,omitempty" example:"Monthly (2026)"`
	// false archives, true restores.
	IsActive *bool `json:"is_active,omitempty" example:"false"`
}

type PriceResponse struct {
	ID          uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	ProductID   uuid.UUID `json:"product_id" swaggertype:"string" format:"uuid"`
	Nickname    string    `json:"nickname" example:"Monthly"`
	Currency    string    `json:"currency" example:"usd"`
	UnitAmount  int64     `json:"unit_amount" example:"2500"`
	Interval    string    `json:"interval" example:"month"`
	BillingUnit string    `json:"billing_unit" example:"truck"`
	IsActive    bool      `json:"is_active" example:"true"`
	// Stripe's id: find the price in the Stripe Dashboard.
	StripePriceID string    `json:"stripe_price_id" example:"price_1QabcDEFghiJKL"`
	CreatedAt     time.Time `json:"created_at" example:"2026-10-06T11:26:26Z"`
}

type ProductResponse struct {
	ID              uuid.UUID       `json:"id" swaggertype:"string" format:"uuid"`
	Name            string          `json:"name" example:"ELD Fleet plan"`
	Description     string          `json:"description" example:"Hours of service and DVIR for every truck"`
	IsActive        bool            `json:"is_active" example:"true"`
	StripeProductID string          `json:"stripe_product_id" example:"prod_QabcDEFghiJKL"`
	Prices          []PriceResponse `json:"prices"`
	CreatedAt       time.Time       `json:"created_at" example:"2026-10-06T11:26:26Z"`
	UpdatedAt       time.Time       `json:"updated_at" example:"2026-10-06T11:26:26Z"`
}

type ListProductsResponse struct {
	Items    []ProductResponse `json:"items"`
	Page     int               `json:"page" example:"1"`
	PageSize int               `json:"page_size" example:"20"`
	Total    int               `json:"total" example:"3"`
}

func newPriceResponse(pr *model.Price) PriceResponse {
	return PriceResponse{
		ID: pr.ID, ProductID: pr.ProductID, Nickname: pr.Nickname, Currency: pr.Currency,
		UnitAmount: pr.UnitAmount, Interval: pr.Interval, BillingUnit: pr.BillingUnit,
		IsActive: pr.IsActive, StripePriceID: pr.StripePriceID, CreatedAt: pr.CreatedAt.UTC(),
	}
}

func newProductResponse(p *model.Product) ProductResponse {
	prices := make([]PriceResponse, 0, len(p.Prices)) // [] not null
	for i := range p.Prices {
		prices = append(prices, newPriceResponse(&p.Prices[i]))
	}
	return ProductResponse{
		ID: p.ID, Name: p.Name, Description: p.Description, IsActive: p.IsActive,
		StripeProductID: p.StripeProductID, Prices: prices,
		CreatedAt: p.CreatedAt.UTC(), UpdatedAt: p.UpdatedAt.UTC(),
	}
}

// ---------- Handler ----------

type ProductHandler struct {
	svc CatalogService
}

func NewProductHandler(svc CatalogService) *ProductHandler {
	return &ProductHandler{svc: svc}
}

// Create handles POST /api/v1/products.
//
//	@Summary		Create a product
//	@Description	Requires permission: product:create (platform only). Created in Stripe first, then saved here.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateProductRequest	true	"Product to create"
//	@Success		201		{object}	ProductResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission product:create, or not a platform user"
//	@Failure		500		{object}	apierror.Response
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/products [post]
func (h *ProductHandler) Create(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req CreateProductRequest
	if !bindJSON(c, &req) {
		return
	}
	prod, err := h.svc.CreateProduct(c.Request.Context(), p, service.CreateProductInput{
		Name: req.Name, Description: req.Description,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.Header("Location", "/api/v1/products/"+prod.ID.String())
	c.JSON(http.StatusCreated, newProductResponse(prod))
}

// List handles GET /api/v1/products.
//
//	@Summary		List products with their prices
//	@Description	Requires permission: product:read
//	@Description	Company users get only active products with active prices (what they can subscribe to); is_active is ignored for them.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			is_active	query		bool	false	"Only active (true) or archived (false) products (platform users only)"
//	@Param			page		query		int		false	"Page number (1-based)"	default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"		default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListProductsResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission product:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/products [get]
func (h *ProductHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var v validation.Validator
	page, ok := queryInt(c, "page")
	v.Check(ok, "page", "must be an integer")
	size, ok := queryInt(c, "page_size")
	v.Check(ok, "page_size", "must be an integer")
	var isActive *bool
	if s := c.Query("is_active"); s != "" {
		b, err := strconv.ParseBool(s)
		v.Check(err == nil, "is_active", "must be true or false")
		isActive = &b
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	res, err := h.svc.ListProducts(c.Request.Context(), p, service.ProductListParams{
		IsActive: isActive, Page: page, PageSize: size,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]ProductResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newProductResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListProductsResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

// Get handles GET /api/v1/products/:id.
//
//	@Summary		Get a product with its prices
//	@Description	Requires permission: product:read. Archived products are not found for company users.
//	@Tags			billing
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Product ID"	format(uuid)
//	@Success		200	{object}	ProductResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission product:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/products/{id} [get]
func (h *ProductHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	prod, err := h.svc.GetProduct(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newProductResponse(prod))
}

// Update handles PATCH /api/v1/products/:id.
//
//	@Summary		Update a product
//	@Description	Requires permission: product:update (platform only). is_active=false archives, true restores.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string					true	"Product ID"	format(uuid)
//	@Param			body	body		UpdateProductRequest	true	"Fields to change"
//	@Success		200		{object}	ProductResponse
//	@Failure		400		{object}	apierror.Response
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission product:update, or not a platform user"
//	@Failure		404		{object}	apierror.Response
//	@Failure		500		{object}	apierror.Response
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/products/{id} [patch]
func (h *ProductHandler) Update(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateProductRequest
	if !bindJSON(c, &req) {
		return
	}
	prod, err := h.svc.UpdateProduct(c.Request.Context(), p, id, service.UpdateProductInput{
		Name: req.Name, Description: req.Description, IsActive: req.IsActive,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newProductResponse(prod))
}

// Archive handles DELETE /api/v1/products/:id.
//
//	@Summary		Archive a product
//	@Description	Requires permission: product:delete (platform only).
//	@Description	Nothing is deleted: existing subscriptions keep it, new ones cannot choose it. Restore with PATCH is_active=true.
//	@Tags			billing
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Product ID"	format(uuid)
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission product:delete, or not a platform user"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Failure		502	{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/products/{id} [delete]
func (h *ProductHandler) Archive(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.ArchiveProduct(c.Request.Context(), p, id); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// CreatePrice handles POST /api/v1/products/:id/prices.
//
//	@Summary		Add a price to a product
//	@Description	Requires permission: product:create (platform only).
//	@Description	Amount, interval and billing unit can never change afterwards: to change a price, create a new one and archive the old one.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Product ID"	format(uuid)
//	@Param			body	body		CreatePriceRequest	true	"Price to create"
//	@Success		201		{object}	PriceResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON, validation failed, or the product is archived"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission product:create, or not a platform user"
//	@Failure		404		{object}	apierror.Response	"product not found"
//	@Failure		500		{object}	apierror.Response
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/products/{id}/prices [post]
func (h *ProductHandler) CreatePrice(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	productID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req CreatePriceRequest
	if !bindJSON(c, &req) {
		return
	}
	pr, err := h.svc.CreatePrice(c.Request.Context(), p, productID, service.CreatePriceInput{
		Nickname: req.Nickname, UnitAmount: req.UnitAmount, Interval: req.Interval, BillingUnit: req.BillingUnit,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newPriceResponse(pr))
}

// UpdatePrice handles PATCH /api/v1/prices/:id.
//
//	@Summary		Rename, archive or restore a price
//	@Description	Requires permission: product:update (platform only). Only nickname and is_active can change.
//	@Tags			billing
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Price ID"	format(uuid)
//	@Param			body	body		UpdatePriceRequest	true	"Fields to change"
//	@Success		200		{object}	PriceResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON (unit_amount etc. are unknown fields here) or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission product:update, or not a platform user"
//	@Failure		404		{object}	apierror.Response
//	@Failure		500		{object}	apierror.Response
//	@Failure		502		{object}	apierror.Response	"Stripe refused the request or could not be reached"
//	@Router			/api/v1/prices/{id} [patch]
func (h *ProductHandler) UpdatePrice(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdatePriceRequest
	if !bindJSON(c, &req) {
		return
	}
	pr, err := h.svc.UpdatePrice(c.Request.Context(), p, id, service.UpdatePriceInput{
		Nickname: req.Nickname, IsActive: req.IsActive,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newPriceResponse(pr))
}
