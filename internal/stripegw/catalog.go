package stripegw

import (
	"context"

	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Catalog creates and edits products and prices in Stripe.
type Catalog struct {
	c *stripe.Client
}

func NewCatalog(c *stripe.Client) *Catalog {
	return &Catalog{c: c}
}

// CreateProduct creates p in Stripe and returns Stripe's id (prod_...).
//
// The idempotency key is derived from our id: if this exact request is
// retried (by the SDK after a timeout, or by us), Stripe returns the product
// it already created instead of making a second one.
func (g *Catalog) CreateProduct(ctx context.Context, p model.Product) (string, error) {
	params := &stripe.ProductCreateParams{
		Name:     stripe.String(p.Name),
		Metadata: map[string]string{MetaProductID: p.ID.String()},
	}
	if p.Description != "" { // Stripe refuses an empty description
		params.Description = stripe.String(p.Description)
	}
	params.SetIdempotencyKey("product-create-" + p.ID.String())

	created, err := g.c.V1Products.Create(ctx, params)
	if err != nil {
		return "", wrap("create product", err)
	}
	return created.ID, nil
}

// UpdateProduct saves name, description and active state of p in Stripe.
func (g *Catalog) UpdateProduct(ctx context.Context, p model.Product) error {
	params := &stripe.ProductUpdateParams{
		Name:   stripe.String(p.Name),
		Active: stripe.Bool(p.IsActive),
	}
	if p.Description != "" {
		params.Description = stripe.String(p.Description)
	} else {
		// "Remove the description": an empty string is refused, unsetting works.
		params.UnsetFields = []stripe.ProductUpdateParamsUnsetField{stripe.ProductUpdateParamsUnsetFieldDescription}
	}
	if _, err := g.c.V1Products.Update(ctx, p.StripeProductID, params); err != nil {
		return wrap("update product", err)
	}
	return nil
}

// CreatePrice creates pr in Stripe as a recurring price of the product
// stripeProductID, and returns Stripe's id (price_...).
func (g *Catalog) CreatePrice(ctx context.Context, stripeProductID string, pr model.Price) (string, error) {
	params := &stripe.PriceCreateParams{
		Product:    stripe.String(stripeProductID),
		Currency:   stripe.String(pr.Currency),
		UnitAmount: stripe.Int64(pr.UnitAmount),
		Recurring:  &stripe.PriceCreateRecurringParams{Interval: stripe.String(pr.Interval)},
		Metadata: map[string]string{
			MetaPriceID:     pr.ID.String(),
			MetaBillingUnit: pr.BillingUnit,
		},
	}
	if pr.Nickname != "" {
		params.Nickname = stripe.String(pr.Nickname)
	}
	params.SetIdempotencyKey("price-create-" + pr.ID.String())

	created, err := g.c.V1Prices.Create(ctx, params)
	if err != nil {
		return "", wrap("create price", err)
	}
	return created.ID, nil
}

// UpdatePrice saves nickname and active state of pr in Stripe (nothing else
// of a price can change).
func (g *Catalog) UpdatePrice(ctx context.Context, pr model.Price) error {
	params := &stripe.PriceUpdateParams{
		Active: stripe.Bool(pr.IsActive),
		// An empty string clears the nickname (Stripe's form-encoding rule).
		Nickname: stripe.String(pr.Nickname),
	}
	if _, err := g.c.V1Prices.Update(ctx, pr.StripePriceID, params); err != nil {
		return wrap("update price", err)
	}
	return nil
}
