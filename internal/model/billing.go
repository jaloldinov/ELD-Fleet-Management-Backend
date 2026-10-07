package model

import (
	"time"

	"github.com/google/uuid"
)

// Billing intervals: how often a price is charged. Every item of one
// subscription must have the same interval (a Stripe rule).
const (
	IntervalMonth = "month"
	IntervalYear  = "year"
)

// Billing units: what the quantity of a price counts.
const (
	BillingUnitCompany = "company" // a flat amount per company (quantity 1)
	BillingUnitTruck   = "truck"   // per active truck (quantity = fleet size)
)

// CurrencyUSD is the only currency for now (US market).
const CurrencyUSD = "usd"

// Product is something a company can subscribe to, e.g. "ELD Fleet plan".
// Stripe holds the original; this is our copy.
type Product struct {
	ID              uuid.UUID
	StripeProductID string // prod_...
	Name            string
	Description     string
	IsActive        bool // false = archived: cannot be chosen any more
	CreatedAt       time.Time
	UpdatedAt       time.Time

	Prices []Price // filled when reading; never written through Product
}

// Price is how much a product costs and how often. Amount, currency,
// interval and unit never change (a new amount is a new price).
type Price struct {
	ID            uuid.UUID
	ProductID     uuid.UUID
	StripePriceID string // price_...
	Nickname      string // "Monthly", "Yearly"
	Currency      string // CurrencyUSD
	UnitAmount    int64  // cents: 2500 = $25.00
	Interval      string // IntervalMonth or IntervalYear
	BillingUnit   string // BillingUnitCompany or BillingUnitTruck
	IsActive      bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ProductFilter narrows a product list. The zero value means "no filter".
type ProductFilter struct {
	IsActive *bool
	// ActivePricesOnly leaves archived prices out of Product.Prices
	// (company users only see what they can still choose).
	ActivePricesOnly bool
}

// PaymentMethod is a saved card, as Stripe describes it. Card numbers never
// reach our server: we only ever see the brand and the last 4 digits.
// Not stored in our database (read from Stripe when needed).
type PaymentMethod struct {
	ID         string // pm_...
	CustomerID string // cus_... it is saved to; "" if none
	Brand      string // visa, mastercard, amex, ...
	Last4      string
	ExpMonth   int
	ExpYear    int
	IsDefault  bool // charged for invoices unless another is chosen
}
