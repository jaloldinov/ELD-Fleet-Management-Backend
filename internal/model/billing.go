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

	// A plan change scheduled for the next renewal (nil/empty = none).
	StripeScheduleID  *string    // sub_sched_...
	ScheduledChangeAt *time.Time // when ScheduledItems replace Items
	ScheduledItems    []SubscriptionItem
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
	Metadata          map[string]string
	Status            string
	CancelAtPeriodEnd bool
	CanceledAt        *time.Time
	Items             []StripeSubscriptionItem
	LatestInvoice     *StripeInvoice // nil if Stripe has none
	// PendingUpdate: a plan change is waiting for its invoice to be paid
	// (pay first applies to upgrades too). Until then the old items stay.
	PendingUpdate bool
	// Schedule is the subscription schedule attached to it, if any.
	Schedule *StripeSchedule
}

// StripeSchedule is a Stripe subscription schedule (sub_sched_...): the
// current plan until a date, then another one.
type StripeSchedule struct {
	ID string
	// The phase running now (Unix seconds); updates must start from it.
	CurrentPhaseStart int64
	CurrentPhaseEnd   int64
	// The next phase: when it starts and its items (price + quantity).
	// NextAt nil = nothing scheduled any more (the last phase is running).
	NextAt    *time.Time
	NextItems []StripeSubscriptionItem
}

// StripeScheduleChange is what is sent to Stripe to schedule a plan change
// at the end of the current period.
type StripeScheduleChange struct {
	SubscriptionID string
	ScheduleID     string                   // "" = create one from the subscription
	CurrentItems   []StripeSubscriptionItem // the plan until the period ends
	NextItems      []StripeSubscriptionItem // the plan after it
	NextInterval   string                   // IntervalMonth / IntervalYear of NextItems
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

// StripeMetaSubscriptionID is the Stripe metadata key holding our
// subscription id (set at creation; read when a webhook arrives first).
const StripeMetaSubscriptionID = "eld_subscription_id"

// StripeSubscriptionCreate is what is sent to Stripe to start a
// subscription.
type StripeSubscriptionCreate struct {
	ID         uuid.UUID // ours: metadata and idempotency key
	CompanyID  uuid.UUID
	CustomerID string                   // cus_...
	Items      []StripeSubscriptionItem // StripePriceID + Quantity
}

// Billing statuses of a company (companies.billing_status): what its
// subscription allows it to do.
const (
	BillingNone    = "none"     // no subscription (never, or it ended)
	BillingPending = "pending"  // subscribed, first invoice unpaid
	BillingActive  = "active"   // paid up
	BillingPastDue = "past_due" // a renewal failed; grace period while Stripe retries
	BillingBlocked = "blocked"  // Stripe gave up (unpaid) or collection is paused
)

// BillingStatusFor maps the status of a company's current subscription to
// its billing status. current is nil when it has none.
func BillingStatusFor(current *Subscription) string {
	if current == nil {
		return BillingNone
	}
	switch current.Status {
	case SubscriptionActive, SubscriptionTrialing:
		return BillingActive
	case SubscriptionPastDue:
		return BillingPastDue
	case SubscriptionIncomplete:
		return BillingPending
	case SubscriptionUnpaid, SubscriptionPaused:
		return BillingBlocked
	default: // canceled, incomplete_expired: not current anyway
		return BillingNone
	}
}

// BillingAllowsUse reports whether a company with this billing status may
// use the product. past_due is allowed on purpose: one expired card must
// not stop a fleet the same day (the grace period).
func BillingAllowsUse(status string) bool {
	return status == BillingActive || status == BillingPastDue
}

// StripeEvent is a webhook from Stripe, reduced to what we act on.
type StripeEvent struct {
	ID         string // evt_...
	Type       string // invoice.payment_failed, ...
	ObjectID   string // id of the object it is about: sub_..., in_..., ...
	ObjectType string // subscription, invoice, ...
	Livemode   bool
}

// StripeItemChange is one line of a plan change sent to Stripe:
//   - ID set, Deleted:  remove that item
//   - ID set:           keep that item, with Quantity
//   - ID empty:         add StripePriceID with Quantity
type StripeItemChange struct {
	ID            string // si_... of an existing item
	StripePriceID string
	Quantity      int
	Deleted       bool
}

// ProrationPreview is what a plan change would cost, as Stripe computes it.
type ProrationPreview struct {
	// ProrationDate is the moment the preview was computed for (Unix
	// seconds). Confirming the change with the same date charges exactly
	// the previewed amount.
	ProrationDate int64
	// Total of what the change itself costs, in cents: positive = a
	// charge; negative = credit for unused time, added to the customer
	// balance (it pays the next invoices). Prorations already waiting for
	// the next renewal (e.g. trucks added this month) are not included.
	Total int64
	// AmountDue is what the card is charged now: Total minus the credit the
	// customer already has, never negative.
	AmountDue int64
	Lines     []PreviewLine
}

type PreviewLine struct {
	Description string // "Unused time on ELD Fleet plan after 7 Oct 2026"
	Amount      int64  // cents; negative = credit
	Proration   bool   // part of the change, not a regular period charge
}
