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

// Subscription statuses, as Stripe names them.
const (
	SubscriptionIncomplete        = "incomplete"         // first invoice not paid yet
	SubscriptionIncompleteExpired = "incomplete_expired" // first invoice never paid
	SubscriptionTrialing          = "trialing"
	SubscriptionActive            = "active"
	SubscriptionPastDue           = "past_due" // a renewal failed, Stripe retries
	SubscriptionUnpaid            = "unpaid"   // Stripe gave up retrying
	SubscriptionCanceled          = "canceled"
	SubscriptionPaused            = "paused"
)

// SubscriptionEnded reports whether a subscription with this status is
// history: a company with only ended subscriptions may start a new one.
func SubscriptionEnded(status string) bool {
	return status == SubscriptionCanceled || status == SubscriptionIncompleteExpired
}

// Subscription is a company's contract: prices × quantities, billed every
// interval. Stripe holds the original; this is our copy.
type Subscription struct {
	ID                   uuid.UUID
	CompanyID            uuid.UUID
	StripeSubscriptionID string // sub_...
	Status               string
	Interval             string     // IntervalMonth / IntervalYear
	CurrentPeriodStart   *time.Time // the period being paid for now
	CurrentPeriodEnd     *time.Time
	CancelAtPeriodEnd    bool
	CanceledAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time

	Items []SubscriptionItem
}

// SubscriptionItem is one price of a subscription and its quantity.
type SubscriptionItem struct {
	ID                       uuid.UUID
	StripeSubscriptionItemID string // si_...
	Price                    Price  // filled when reading
	ProductName              string // filled when reading
	Quantity                 int
}

// SubscriptionFilter narrows a subscription list. Zero value = no filter.
type SubscriptionFilter struct {
	CompanyID *uuid.UUID
	Status    string // "" = any
}

// StripeSubscription is a subscription as Stripe returns it: what the
// gateway reads, and what our copy is built from.
type StripeSubscription struct {
	ID                string // sub_...
	CustomerID        string // cus_...
	Status            string
	CancelAtPeriodEnd bool
	CanceledAt        *time.Time
	Items             []StripeSubscriptionItem
	LatestInvoice     *StripeInvoice // nil if Stripe has none
}

type StripeSubscriptionItem struct {
	ID            string // si_...
	StripePriceID string // price_...
	Quantity      int
	PeriodStart   *time.Time
	PeriodEnd     *time.Time
}

// StripeInvoice is the part of an invoice the payment flow needs.
type StripeInvoice struct {
	ID        string // in_...
	Status    string // draft, open, paid, void, uncollectible
	AmountDue int64  // cents
	// ClientSecret lets the browser confirm the payment (Stripe.js),
	// including 3-D Secure. Empty when there is nothing to pay.
	ClientSecret string
}

// CatalogPrice is a price with what a subscription needs to know about its
// product.
type CatalogPrice struct {
	Price
	ProductName   string
	ProductActive bool
}

// StripeSubscriptionCreate is what is sent to Stripe to start a
// subscription.
type StripeSubscriptionCreate struct {
	ID              uuid.UUID // ours: metadata and idempotency key
	CompanyID       uuid.UUID
	CustomerID      string                   // cus_...
	PaymentMethodID string                   // pm_...: the card the first invoice will be paid with
	Items           []StripeSubscriptionItem // StripePriceID + Quantity
}
