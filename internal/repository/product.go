package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const productColumns = `id, stripe_product_id, name, description, is_active, created_at, updated_at`

const priceColumns = `id, product_id, stripe_price_id, nickname, currency, unit_amount,
	billing_interval, billing_unit, is_active, created_at, updated_at`

// ProductRepository stores our copy of the Stripe catalog: products and
// their prices.
type ProductRepository struct {
	db DBTX
}

func NewProductRepository(db DBTX) *ProductRepository {
	return &ProductRepository{db: db}
}

func (r *ProductRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// CreateProduct inserts p with the id the service chose (it was already
// sent to Stripe as metadata, so the database must not invent another).
func (r *ProductRepository) CreateProduct(ctx context.Context, p model.Product) (*model.Product, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO products (id, stripe_product_id, name, description)
		VALUES ($1, $2, $3, $4)
		RETURNING `+productColumns,
		p.ID, p.StripeProductID, p.Name, p.Description,
	)
	created, err := scanProduct(row)
	if err != nil {
		return nil, mapProductError("create product", err)
	}
	created.Prices = []model.Price{}
	return created, nil
}

// GetProduct returns a product with its prices (only active ones if
// activePricesOnly).
func (r *ProductRepository) GetProduct(ctx context.Context, id uuid.UUID, activePricesOnly bool) (*model.Product, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1`, id)
	p, err := scanProduct(row)
	if err != nil {
		return nil, mapProductError("get product", err)
	}
	prices, err := r.pricesOf(ctx, []uuid.UUID{p.ID}, activePricesOnly)
	if err != nil {
		return nil, err
	}
	p.Prices = prices[p.ID]
	return p, nil
}

// ListProducts returns one page of products (newest first), each with its
// prices: two queries in total, never one per product.
func (r *ProductRepository) ListProducts(ctx context.Context, f model.ProductFilter, limit, offset int) ([]model.Product, int, error) {
	const where = ` WHERE ($1::boolean IS NULL OR is_active = $1)`

	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM products`+where, f.IsActive).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count products: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, `SELECT `+productColumns+` FROM products`+where+`
		ORDER BY id DESC
		LIMIT $2 OFFSET $3`,
		f.IsActive, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list products: %w", err)
	}
	products, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Product, error) {
		p, err := scanProduct(row)
		if err != nil {
			return model.Product{}, err
		}
		return *p, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan products: %w", err)
	}

	ids := make([]uuid.UUID, len(products))
	for i := range products {
		ids[i] = products[i].ID
	}
	prices, err := r.pricesOf(ctx, ids, f.ActivePricesOnly)
	if err != nil {
		return nil, 0, err
	}
	for i := range products {
		products[i].Prices = prices[products[i].ID]
	}
	return products, total, nil
}

// UpdateProduct saves the editable fields.
func (r *ProductRepository) UpdateProduct(ctx context.Context, p model.Product) (*model.Product, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE products SET name = $2, description = $3, is_active = $4
		WHERE id = $1
		RETURNING `+productColumns,
		p.ID, p.Name, p.Description, p.IsActive,
	)
	updated, err := scanProduct(row)
	if err != nil {
		return nil, mapProductError("update product", err)
	}
	return updated, nil
}

// CreatePrice inserts pr with the id the service chose.
func (r *ProductRepository) CreatePrice(ctx context.Context, pr model.Price) (*model.Price, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO prices (id, product_id, stripe_price_id, nickname, currency, unit_amount,
		                    billing_interval, billing_unit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+priceColumns,
		pr.ID, pr.ProductID, pr.StripePriceID, pr.Nickname, pr.Currency, pr.UnitAmount,
		pr.Interval, pr.BillingUnit,
	)
	created, err := scanPrice(row)
	if err != nil {
		return nil, mapProductError("create price", err)
	}
	return created, nil
}

func (r *ProductRepository) GetPrice(ctx context.Context, id uuid.UUID) (*model.Price, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+priceColumns+` FROM prices WHERE id = $1`, id)
	pr, err := scanPrice(row)
	if err != nil {
		return nil, mapProductError("get price", err)
	}
	return pr, nil
}

// UpdatePrice saves the only editable fields: amount, currency, interval
// and unit are fixed for the life of a price (as in Stripe).
func (r *ProductRepository) UpdatePrice(ctx context.Context, pr model.Price) (*model.Price, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE prices SET nickname = $2, is_active = $3
		WHERE id = $1
		RETURNING `+priceColumns,
		pr.ID, pr.Nickname, pr.IsActive,
	)
	updated, err := scanPrice(row)
	if err != nil {
		return nil, mapProductError("update price", err)
	}
	return updated, nil
}

// pricesOf loads the prices of several products in one query, grouped by
// product. Every product in productIDs gets a (possibly empty) slice, so the
// API returns [] instead of null.
func (r *ProductRepository) pricesOf(ctx context.Context, productIDs []uuid.UUID, activeOnly bool) (map[uuid.UUID][]model.Price, error) {
	out := make(map[uuid.UUID][]model.Price, len(productIDs))
	for _, id := range productIDs {
		out[id] = []model.Price{}
	}
	if len(productIDs) == 0 {
		return out, nil
	}

	// Monthly before yearly, cheapest first: the order a pricing page shows.
	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+priceColumns+` FROM prices
		WHERE product_id = ANY($1) AND (NOT $2 OR is_active)
		ORDER BY product_id, billing_interval, unit_amount, id`,
		productIDs, activeOnly,
	)
	if err != nil {
		return nil, fmt.Errorf("list prices: %w", err)
	}
	prices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Price, error) {
		pr, err := scanPrice(row)
		if err != nil {
			return model.Price{}, err
		}
		return *pr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan prices: %w", err)
	}
	for _, pr := range prices {
		out[pr.ProductID] = append(out[pr.ProductID], pr)
	}
	return out, nil
}

func scanProduct(row pgx.Row) (*model.Product, error) {
	var p model.Product
	err := row.Scan(&p.ID, &p.StripeProductID, &p.Name, &p.Description, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanPrice(row pgx.Row) (*model.Price, error) {
	var pr model.Price
	err := row.Scan(&pr.ID, &pr.ProductID, &pr.StripePriceID, &pr.Nickname, &pr.Currency, &pr.UnitAmount,
		&pr.Interval, &pr.BillingUnit, &pr.IsActive, &pr.CreatedAt, &pr.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

func mapProductError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}
