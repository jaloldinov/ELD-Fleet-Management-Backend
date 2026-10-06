package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// ProductRepository is what CatalogService needs from the data layer.
type ProductRepository interface {
	CreateProduct(ctx context.Context, p model.Product) (*model.Product, error)
	GetProduct(ctx context.Context, id uuid.UUID, activePricesOnly bool) (*model.Product, error)
	ListProducts(ctx context.Context, f model.ProductFilter, limit, offset int) ([]model.Product, int, error)
	UpdateProduct(ctx context.Context, p model.Product) (*model.Product, error)
	CreatePrice(ctx context.Context, pr model.Price) (*model.Price, error)
	GetPrice(ctx context.Context, id uuid.UUID) (*model.Price, error)
	UpdatePrice(ctx context.Context, pr model.Price) (*model.Price, error)
}

// CatalogGateway is what CatalogService needs from Stripe (stripegw.Catalog).
type CatalogGateway interface {
	CreateProduct(ctx context.Context, p model.Product) (stripeID string, err error)
	UpdateProduct(ctx context.Context, p model.Product) error
	CreatePrice(ctx context.Context, stripeProductID string, pr model.Price) (stripeID string, err error)
	UpdatePrice(ctx context.Context, pr model.Price) error
}

const (
	productNameMaxLen        = 200
	productDescriptionMaxLen = 1000
	priceNicknameMaxLen      = 100
	// Stripe's maximum for USD: 99,999,999 cents ($999,999.99).
	priceMaxUnitAmount = 99_999_999
)

type CreateProductInput struct {
	Name        string
	Description string
}

// UpdateProductInput: nil = unchanged.
type UpdateProductInput struct {
	Name        *string
	Description *string
	IsActive    *bool
}

type CreatePriceInput struct {
	Nickname    string
	UnitAmount  int64  // cents
	Interval    string // model.IntervalMonth / IntervalYear
	BillingUnit string // model.BillingUnitCompany / BillingUnitTruck
}

// UpdatePriceInput: nil = unchanged. Amount, currency, interval and unit
// cannot change (a new amount is a new price).
type UpdatePriceInput struct {
	Nickname *string
	IsActive *bool
}

type ProductListParams struct {
	IsActive *bool // platform users only; company users always get active ones
	Page     int
	PageSize int
}

type ProductPage struct {
	Items    []model.Product
	Total    int
	Page     int
	PageSize int
}

// CatalogService manages the billing catalog: products and their prices.
//
// Writes are platform-only (also enforced here, not just by permissions).
// Every write goes to Stripe FIRST, then to our database:
//   - Stripe fails: nothing was saved anywhere, the caller gets the error.
//   - Stripe succeeds, our database fails: Stripe has an object we do not.
//     The error names its Stripe id; it also carries our id in metadata,
//     which the webhook sync (step 6e) uses to repair our copy.
//
// The reverse order (database first) has the same problem the other way
// round, and a database transaction cannot include a call to Stripe.
type CatalogService struct {
	repo    ProductRepository
	gateway CatalogGateway
	audit   *Auditor
}

func NewCatalogService(repo ProductRepository, gateway CatalogGateway, audit *Auditor) *CatalogService {
	return &CatalogService{repo: repo, gateway: gateway, audit: audit}
}

// ---------- products ----------

func (s *CatalogService) CreateProduct(ctx context.Context, p auth.Principal, in CreateProductInput) (*model.Product, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	prod := model.Product{
		ID:          id,
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
		IsActive:    true,
	}
	if err := validateProduct(prod); err != nil {
		return nil, err
	}

	if prod.StripeProductID, err = s.gateway.CreateProduct(ctx, prod); err != nil {
		return nil, err
	}

	var created *model.Product
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.repo.CreateProduct(ctx, prod); err != nil {
			return err
		}
		return s.recordProduct(ctx, p, "product.create", created, productChanges(model.Product{}, *created))
	})
	if err != nil {
		return nil, fmt.Errorf("product created in Stripe as %s but not saved here: %w", prod.StripeProductID, err)
	}
	return created, nil
}

// GetProduct: company users see only active products, with active prices.
// An archived product looks like a missing one to them.
func (s *CatalogService) GetProduct(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Product, error) {
	prod, err := s.repo.GetProduct(ctx, id, !p.IsPlatformUser())
	if err != nil {
		return nil, err
	}
	if !p.IsPlatformUser() && !prod.IsActive {
		return nil, model.ErrNotFound
	}
	return prod, nil
}

func (s *CatalogService) ListProducts(ctx context.Context, p auth.Principal, params ProductListParams) (*ProductPage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})

	f := model.ProductFilter{IsActive: params.IsActive}
	if !p.IsPlatformUser() {
		// Company users choose a plan: only what can still be chosen.
		active := true
		f = model.ProductFilter{IsActive: &active, ActivePricesOnly: true}
	}

	items, total, err := s.repo.ListProducts(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &ProductPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

func (s *CatalogService) UpdateProduct(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdateProductInput) (*model.Product, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	if in == (UpdateProductInput{}) {
		return nil, &validation.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	prod, err := s.repo.GetProduct(ctx, id, false)
	if err != nil {
		return nil, err
	}
	before := *prod
	if in.Name != nil {
		prod.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		prod.Description = strings.TrimSpace(*in.Description)
	}
	if in.IsActive != nil {
		prod.IsActive = *in.IsActive
	}
	if err := validateProduct(*prod); err != nil {
		return nil, err
	}
	return s.saveProduct(ctx, p, "product.update", before, *prod)
}

// ArchiveProduct hides a product from new subscriptions. Nothing is
// deleted: existing subscriptions and invoices keep pointing to it, and it
// can be restored with UpdateProduct (is_active=true). Archiving an
// archived product succeeds and changes nothing.
func (s *CatalogService) ArchiveProduct(ctx context.Context, p auth.Principal, id uuid.UUID) error {
	if !p.IsPlatformUser() {
		return model.ErrForbidden
	}
	prod, err := s.repo.GetProduct(ctx, id, false)
	if err != nil {
		return err
	}
	if !prod.IsActive {
		return nil
	}
	before := *prod
	prod.IsActive = false
	_, err = s.saveProduct(ctx, p, "product.archive", before, *prod)
	return err
}

// saveProduct sends the change to Stripe, then saves it here with its
// audit entry.
func (s *CatalogService) saveProduct(ctx context.Context, p auth.Principal, action string, before, after model.Product) (*model.Product, error) {
	ch := productChanges(before, after)
	if len(ch) == 0 {
		return &before, nil // nothing changed: no Stripe call, no audit row
	}
	if err := s.gateway.UpdateProduct(ctx, after); err != nil {
		return nil, err
	}

	var saved *model.Product
	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.repo.UpdateProduct(ctx, after); err != nil {
			return err
		}
		return s.recordProduct(ctx, p, action, saved, ch)
	})
	if err != nil {
		return nil, fmt.Errorf("product %s changed in Stripe but not saved here: %w", after.StripeProductID, err)
	}
	saved.Prices = before.Prices
	return saved, nil
}

// ---------- prices ----------

// CreatePrice adds a recurring price to an active product.
func (s *CatalogService) CreatePrice(ctx context.Context, p auth.Principal, productID uuid.UUID, in CreatePriceInput) (*model.Price, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	prod, err := s.repo.GetProduct(ctx, productID, false)
	if err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	pr := model.Price{
		ID:          id,
		ProductID:   prod.ID,
		Nickname:    strings.TrimSpace(in.Nickname),
		Currency:    model.CurrencyUSD,
		UnitAmount:  in.UnitAmount,
		Interval:    in.Interval,
		BillingUnit: in.BillingUnit,
		IsActive:    true,
	}

	var v validation.Validator
	// A price on an archived product could never be chosen.
	v.Check(prod.IsActive, "product", "is archived: restore it before adding prices")
	v.Check(pr.UnitAmount >= 0 && pr.UnitAmount <= priceMaxUnitAmount, "unit_amount",
		"must be between 0 and 99999999 (cents)")
	v.Check(pr.Interval == model.IntervalMonth || pr.Interval == model.IntervalYear, "interval",
		"must be month or year")
	v.Check(pr.BillingUnit == model.BillingUnitCompany || pr.BillingUnit == model.BillingUnitTruck, "billing_unit",
		"must be company (flat) or truck (per active truck)")
	checkNickname(&v, pr.Nickname)
	if err := v.Err(); err != nil {
		return nil, err
	}

	if pr.StripePriceID, err = s.gateway.CreatePrice(ctx, prod.StripeProductID, pr); err != nil {
		return nil, err
	}

	var created *model.Price
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.repo.CreatePrice(ctx, pr); err != nil {
			return err
		}
		return s.recordPrice(ctx, p, "price.create", created, priceChanges(model.Price{}, *created))
	})
	if err != nil {
		return nil, fmt.Errorf("price created in Stripe as %s but not saved here: %w", pr.StripePriceID, err)
	}
	return created, nil
}

// UpdatePrice renames a price or archives/restores it. To change the
// amount, create a new price and archive this one.
func (s *CatalogService) UpdatePrice(ctx context.Context, p auth.Principal, id uuid.UUID, in UpdatePriceInput) (*model.Price, error) {
	if !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	if in == (UpdatePriceInput{}) {
		return nil, &validation.ValidationError{Fields: map[string]string{"body": "at least one field must be provided"}}
	}

	pr, err := s.repo.GetPrice(ctx, id)
	if err != nil {
		return nil, err
	}
	before := *pr
	if in.Nickname != nil {
		pr.Nickname = strings.TrimSpace(*in.Nickname)
	}
	if in.IsActive != nil {
		pr.IsActive = *in.IsActive
	}
	var v validation.Validator
	checkNickname(&v, pr.Nickname)
	if err := v.Err(); err != nil {
		return nil, err
	}

	ch := priceChanges(before, *pr)
	if len(ch) == 0 {
		return &before, nil
	}
	if err := s.gateway.UpdatePrice(ctx, *pr); err != nil {
		return nil, err
	}

	var saved *model.Price
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.repo.UpdatePrice(ctx, *pr); err != nil {
			return err
		}
		return s.recordPrice(ctx, p, "price.update", saved, ch)
	})
	if err != nil {
		return nil, fmt.Errorf("price %s changed in Stripe but not saved here: %w", pr.StripePriceID, err)
	}
	return saved, nil
}

// ---------- helpers ----------

func validateProduct(prod model.Product) error {
	var v validation.Validator
	v.Check(prod.Name != "", "name", "is required")
	v.Check(utf8.RuneCountInString(prod.Name) <= productNameMaxLen, "name", "must be at most 200 characters")
	v.Check(utf8.RuneCountInString(prod.Description) <= productDescriptionMaxLen, "description",
		"must be at most 1000 characters")
	return v.Err()
}

func checkNickname(v *validation.Validator, nickname string) {
	v.Check(utf8.RuneCountInString(nickname) <= priceNicknameMaxLen, "nickname", "must be at most 100 characters")
}

// Catalog changes are platform-level: no company_id in the audit entry.
func (s *CatalogService) recordProduct(ctx context.Context, p auth.Principal, action string, prod *model.Product, ch model.AuditChanges) error {
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: action, Entity: model.AuditProduct, EntityID: prod.ID.String(), Changes: ch,
	})
}

func (s *CatalogService) recordPrice(ctx context.Context, p auth.Principal, action string, pr *model.Price, ch model.AuditChanges) error {
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: action, Entity: model.AuditPrice, EntityID: pr.ID.String(), Changes: ch,
	})
}

func productChanges(before, after model.Product) model.AuditChanges {
	ch := model.AuditChanges{}
	ch.Set("name", before.Name, after.Name)
	ch.Set("description", before.Description, after.Description)
	ch.Set("is_active", before.IsActive, after.IsActive)
	ch.Set("stripe_product_id", before.StripeProductID, after.StripeProductID)
	return ch
}

func priceChanges(before, after model.Price) model.AuditChanges {
	ch := model.AuditChanges{}
	ch.Set("product_id", nilIfZero(before.ProductID), nilIfZero(after.ProductID))
	ch.Set("nickname", before.Nickname, after.Nickname)
	ch.Set("currency", before.Currency, after.Currency)
	ch.Set("unit_amount", before.UnitAmount, after.UnitAmount)
	ch.Set("interval", before.Interval, after.Interval)
	ch.Set("billing_unit", before.BillingUnit, after.BillingUnit)
	ch.Set("is_active", before.IsActive, after.IsActive)
	ch.Set("stripe_price_id", before.StripePriceID, after.StripePriceID)
	return ch
}

// nilIfZero keeps "no value" out of the audit log as null, not as the
// all-zero UUID.
func nilIfZero(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id.String()
}
