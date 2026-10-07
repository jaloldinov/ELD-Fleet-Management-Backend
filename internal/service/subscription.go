package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// SubscriptionStore is what SubscriptionService needs from the data layer.
type SubscriptionStore interface {
	Upsert(ctx context.Context, s model.Subscription) (*model.Subscription, error)
	GetByStripeID(ctx context.Context, stripeID string) (*model.Subscription, error)
	GetCurrent(ctx context.Context, companyID uuid.UUID) (*model.Subscription, error)
	List(ctx context.Context, f model.SubscriptionFilter, limit, offset int) ([]model.Subscription, int, error)
}

// SubscriptionPrices looks up catalog prices (repository.ProductRepository).
type SubscriptionPrices interface {
	GetCatalogPrices(ctx context.Context, ids []uuid.UUID) ([]model.CatalogPrice, error)
	GetCatalogPricesByStripeIDs(ctx context.Context, stripeIDs []string) ([]model.CatalogPrice, error)
}

// SubscriptionCompanies is what SubscriptionService needs from the company
// store (repository.CompanyRepository).
type SubscriptionCompanies interface {
	GetByStripeCustomerID(ctx context.Context, customerID string) (*model.Company, error)
	SetBillingStatus(ctx context.Context, id uuid.UUID, status string) (old string, err error)
}

// ErrNotOurs: a Stripe object that belongs to none of our companies (made
// in the Dashboard, by `stripe trigger`, or by another app on the same
// Stripe account). Webhooks about it are ignored.
var ErrNotOurs = errors.New("the stripe object belongs to none of our companies")

// TruckCounter counts the trucks a per-truck price is billed for.
type TruckCounter interface {
	CountActive(ctx context.Context, companyID uuid.UUID) (int, error)
}

// SubscriptionCustomers is what SubscriptionService needs from
// CustomerService: the company's Stripe customer and its cards.
type SubscriptionCustomers interface {
	EnsureCustomer(ctx context.Context, p auth.Principal, companyID uuid.UUID) (string, error)
	ListPaymentMethods(ctx context.Context, p auth.Principal, companyID uuid.UUID) ([]model.PaymentMethod, error)
}

// SubscriptionGateway is what SubscriptionService needs from Stripe
// (stripegw.Subscriptions).
type SubscriptionGateway interface {
	CreateSubscription(ctx context.Context, in model.StripeSubscriptionCreate) (*model.StripeSubscription, error)
	GetSubscription(ctx context.Context, id string) (*model.StripeSubscription, error)
	PreviewChange(ctx context.Context, subscriptionID, customerID string, items []model.StripeItemChange) (*model.ProrationPreview, error)
	ChangeItems(ctx context.Context, subscriptionID string, items []model.StripeItemChange, prorationDate int64) (*model.StripeSubscription, error)
	SetQuantities(ctx context.Context, subscriptionID string, items []model.StripeItemChange) (*model.StripeSubscription, error)
	SetCancelAtPeriodEnd(ctx context.Context, subscriptionID string, cancel bool) (*model.StripeSubscription, error)
	CancelNow(ctx context.Context, subscriptionID string) (*model.StripeSubscription, error)
	ScheduleChange(ctx context.Context, in model.StripeScheduleChange) (*model.StripeSubscription, error)
	ReleaseSchedule(ctx context.Context, scheduleID, subscriptionID string) (*model.StripeSubscription, error)
}

// Stripe allows at most 20 items per subscription.
const subscriptionMaxItems = 20

type CreateSubscriptionInput struct {
	PriceIDs []uuid.UUID // one per product the company wants
}

// ChangeSubscriptionInput is the WHOLE new plan: every price the company
// wants after the change (prices left out are removed).
type ChangeSubscriptionInput struct {
	PriceIDs []uuid.UUID
	// ProrationDate from the preview the user saw (Unix seconds); nil =
	// now. Reusing it makes the charge equal the previewed amount.
	ProrationDate *int64
}

// prorationDateMaxAge: a preview older than this must be redone, so the
// user never confirms a quote that no longer matches.
const prorationDateMaxAge = time.Hour

type SubscriptionListParams struct {
	CompanyID *uuid.UUID // platform users only
	Status    string
	Page      int
	PageSize  int
}

type SubscriptionPage struct {
	Items    []model.Subscription
	Total    int
	Page     int
	PageSize int
}

// PaymentDue is what the browser needs to pay an invoice with Stripe.js.
type PaymentDue struct {
	InvoiceID       string
	AmountDue       int64  // cents
	ClientSecret    string // for stripe.confirmPayment
	PaymentMethodID string // the card to pay with
}

// SubscriptionState is a subscription plus, while its first invoice is
// unpaid, how to pay it.
type SubscriptionState struct {
	Subscription *model.Subscription
	Payment      *PaymentDue // nil = nothing to pay now
}

// SubscriptionService starts and reads companies' subscriptions.
//
// "Pay first, then use": a new subscription starts as incomplete with an
// open first invoice. The browser pays it (Stripe.js, 3-D Secure if the
// bank asks), then calls Refresh; Stripe now says "active". Unpaid, Stripe
// expires it after 23 hours (incomplete_expired) and the company may try
// again.
//
// Our copy is always rebuilt from what Stripe returns (sync): after
// Create, on Refresh, and (step 6e) when a webhook says something changed.
type SubscriptionService struct {
	store     SubscriptionStore
	companies SubscriptionCompanies
	prices    SubscriptionPrices
	trucks    TruckCounter
	customers SubscriptionCustomers
	gateway   SubscriptionGateway
	audit     *Auditor
}

func NewSubscriptionService(store SubscriptionStore, companies SubscriptionCompanies, prices SubscriptionPrices,
	trucks TruckCounter, customers SubscriptionCustomers, gateway SubscriptionGateway, audit *Auditor) *SubscriptionService {
	return &SubscriptionService{store: store, companies: companies, prices: prices, trucks: trucks,
		customers: customers, gateway: gateway, audit: audit}
}

// Create starts the company's subscription to the given prices.
func (s *SubscriptionService) Create(ctx context.Context, p auth.Principal, companyID uuid.UUID, in CreateSubscriptionInput) (*SubscriptionState, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}

	prices, err := s.validatePrices(ctx, in.PriceIDs)
	if err != nil {
		return nil, err
	}

	// A friendly early answer; the unique index is what really prevents
	// two subscriptions when two requests race.
	if _, err := s.store.GetCurrent(ctx, companyID); err == nil {
		return nil, model.ErrSubscriptionExists
	} else if !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}

	customerID, err := s.customers.EnsureCustomer(ctx, p, companyID) // also: the company exists
	if err != nil {
		return nil, err
	}
	card, err := s.defaultCard(ctx, p, companyID)
	if err != nil {
		return nil, err
	}

	items, err := s.quantities(ctx, companyID, prices)
	if err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	snap, err := s.gateway.CreateSubscription(ctx, model.StripeSubscriptionCreate{
		ID: id, CompanyID: companyID, CustomerID: customerID, Items: items,
	})
	if err != nil {
		return nil, err
	}

	sub, err := s.save(ctx, p, companyID, id, snap, nil)
	if err != nil {
		// Nothing was charged: unpaid, Stripe expires it within 23 hours.
		return nil, fmt.Errorf("subscription created in Stripe as %s but not saved here: %w", snap.ID, err)
	}
	return &SubscriptionState{Subscription: sub, Payment: paymentDue(snap, card)}, nil
}

// Current returns the company's subscription that has not ended, as last
// synced (no Stripe call).
func (s *SubscriptionService) Current(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.Subscription, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	return s.store.GetCurrent(ctx, companyID)
}

// Refresh reads the company's current subscription from Stripe, saves it
// here, and says what is still to pay. The browser calls it right after
// paying; webhooks (step 6e) will do the same on their own.
func (s *SubscriptionService) Refresh(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*SubscriptionState, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil {
		return nil, err
	}
	snap, err := s.gateway.GetSubscription(ctx, current.StripeSubscriptionID)
	if err != nil {
		return nil, err
	}
	sub, err := s.save(ctx, p, companyID, current.ID, snap, current)
	if err != nil {
		return nil, err
	}

	var card string
	if snap.Status == model.SubscriptionIncomplete {
		// The browser may have lost the payment step (closed tab): give it
		// again, with the current default card.
		if card, err = s.defaultCard(ctx, p, companyID); err != nil && !isValidation(err) {
			return nil, err
		}
	}
	return &SubscriptionState{Subscription: sub, Payment: paymentDue(snap, card)}, nil
}

// SyncByStripeID re-reads one Stripe subscription and saves our copy (and
// the company's billing status). Webhooks call it: "something changed
// about sub_123" is all they need to say. Runs as the system.
//
// Returns ErrNotOurs for a subscription of none of our companies.
func (s *SubscriptionService) SyncByStripeID(ctx context.Context, stripeID string) (*model.Subscription, error) {
	snap, err := s.gateway.GetSubscription(ctx, stripeID)
	if errors.Is(err, model.ErrNotFound) {
		return nil, ErrNotOurs // deleted in Stripe, or never existed
	}
	if err != nil {
		return nil, err
	}

	before, err := s.store.GetByStripeID(ctx, stripeID)
	switch {
	case err == nil:
		return s.save(ctx, auth.SystemPrincipal(), before.CompanyID, before.ID, snap, before)
	case !errors.Is(err, model.ErrNotFound):
		return nil, err
	}

	// Not saved here yet: the webhook came before our own save (or the save
	// failed). Find the company by its Stripe customer, reuse our id from
	// the metadata if Stripe has it.
	company, err := s.companies.GetByStripeCustomerID(ctx, snap.CustomerID)
	if errors.Is(err, model.ErrNotFound) {
		return nil, ErrNotOurs
	}
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(snap.Metadata[model.StripeMetaSubscriptionID])
	if err != nil {
		if id, err = uuid.NewV7(); err != nil {
			return nil, err
		}
	}
	return s.save(ctx, auth.SystemPrincipal(), company.ID, id, snap, nil)
}

// ---------- plan changes ----------

// PreviewChange says what changing to the given plan would cost now:
// a charge for an upgrade, a credit for a downgrade. Nothing is changed.
func (s *SubscriptionService) PreviewChange(ctx context.Context, p auth.Principal, companyID uuid.UUID, in ChangeSubscriptionInput) (*model.ProrationPreview, error) {
	current, items, err := s.planChange(ctx, p, companyID, in.PriceIDs)
	if err != nil {
		return nil, err
	}
	customerID, err := s.customers.EnsureCustomer(ctx, p, companyID)
	if err != nil {
		return nil, err
	}
	return s.gateway.PreviewChange(ctx, current.StripeSubscriptionID, customerID, items)
}

// Change moves the company to the given plan. The difference is charged
// (or credited) now; for a charge, the change applies only once it is paid
// (pay first). If the bank wants 3-D Secure, Payment says how to pay in the
// browser, and the old plan stays until then.
func (s *SubscriptionService) Change(ctx context.Context, p auth.Principal, companyID uuid.UUID, in ChangeSubscriptionInput) (*SubscriptionState, error) {
	current, items, err := s.planChange(ctx, p, companyID, in.PriceIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	date := now.Unix()
	if in.ProrationDate != nil {
		d := time.Unix(*in.ProrationDate, 0)
		if d.After(now) || now.Sub(d) > prorationDateMaxAge {
			return nil, &validation.ValidationError{Fields: map[string]string{
				"proration_date": "the preview has expired; preview the change again"}}
		}
		date = *in.ProrationDate
	}

	if current, err = s.releaseIdleSchedule(ctx, p, current); err != nil {
		return nil, err
	}
	snap, err := s.gateway.ChangeItems(ctx, current.StripeSubscriptionID, items, date)
	if err != nil {
		return nil, err
	}
	sub, err := s.save(ctx, p, companyID, current.ID, snap, current)
	if err != nil {
		return nil, err
	}
	var card string
	if snap.PendingUpdate {
		if card, err = s.defaultCard(ctx, p, companyID); err != nil && !isValidation(err) {
			return nil, err
		}
	}
	return &SubscriptionState{Subscription: sub, Payment: paymentDue(snap, card)}, nil
}

// planChange checks a new plan against the current subscription and
// returns the item changes to send to Stripe.
func (s *SubscriptionService) planChange(ctx context.Context, p auth.Principal, companyID uuid.UUID, priceIDs []uuid.UUID) (*model.Subscription, []model.StripeItemChange, error) {
	if !canAccessCompany(p, companyID) {
		return nil, nil, model.ErrNotFound
	}
	prices, err := s.validatePrices(ctx, priceIDs)
	if err != nil {
		return nil, nil, err
	}
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil {
		return nil, nil, err
	}
	switch current.Status {
	case model.SubscriptionActive, model.SubscriptionTrialing:
	case model.SubscriptionIncomplete:
		return nil, nil, &validation.ValidationError{Fields: map[string]string{
			"subscription": "the first invoice is not paid yet: pay it before changing the plan"}}
	default: // past_due, unpaid, paused
		return nil, nil, &validation.ValidationError{Fields: map[string]string{
			"subscription": "a payment is overdue: pay the open invoice before changing the plan"}}
	}

	if err := refuseIfChangeScheduled(current); err != nil {
		return nil, nil, err
	}

	desired, err := s.quantities(ctx, companyID, prices)
	if err != nil {
		return nil, nil, err
	}
	items := diffItems(current.Items, desired)
	if len(items) == 0 {
		return nil, nil, &validation.ValidationError{Fields: map[string]string{
			"price_ids": "this is already the current plan"}}
	}
	return current, items, nil
}

// diffItems turns "current items" and "desired prices with quantities"
// into Stripe item changes. Unchanged items are left out; an empty result
// means nothing changes.
func diffItems(current []model.SubscriptionItem, desired []model.StripeSubscriptionItem) []model.StripeItemChange {
	want := make(map[string]int, len(desired))
	for _, d := range desired {
		want[d.StripePriceID] = d.Quantity
	}
	have := make(map[string]bool, len(current))

	var out []model.StripeItemChange
	for _, it := range current {
		have[it.Price.StripePriceID] = true
		qty, keep := want[it.Price.StripePriceID]
		switch {
		case !keep:
			out = append(out, model.StripeItemChange{ID: it.StripeSubscriptionItemID, Deleted: true})
		case qty != it.Quantity:
			out = append(out, model.StripeItemChange{ID: it.StripeSubscriptionItemID, Quantity: qty})
		}
	}
	for _, d := range desired {
		if !have[d.StripePriceID] {
			out = append(out, model.StripeItemChange{StripePriceID: d.StripePriceID, Quantity: d.Quantity})
		}
	}
	return out
}

// ---------- cancel and resume ----------

// Cancel ends the company's subscription.
//
//   - atPeriodEnd: the company keeps everything until the end of the period
//     it already paid for, and can change its mind (Resume).
//   - now: ends at once; the unused time becomes customer credit. Platform
//     only: ending service today is a support decision.
func (s *SubscriptionService) Cancel(ctx context.Context, p auth.Principal, companyID uuid.UUID, atPeriodEnd bool) (*model.Subscription, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	if !atPeriodEnd && !p.IsPlatformUser() {
		return nil, model.ErrForbidden
	}
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil {
		return nil, err
	}

	if atPeriodEnd {
		// "Change at the renewal" and "end at the renewal" contradict each other.
		if err := refuseIfChangeScheduled(current); err != nil {
			return nil, err
		}
		if current, err = s.releaseIdleSchedule(ctx, p, current); err != nil {
			return nil, err
		}
	}

	var snap *model.StripeSubscription
	if atPeriodEnd {
		if current.Status == model.SubscriptionIncomplete {
			return nil, &validation.ValidationError{Fields: map[string]string{
				"subscription": "not started yet: there is no paid period to end"}}
		}
		if current.CancelAtPeriodEnd {
			return current, nil // already scheduled
		}
		snap, err = s.gateway.SetCancelAtPeriodEnd(ctx, current.StripeSubscriptionID, true)
	} else {
		snap, err = s.gateway.CancelNow(ctx, current.StripeSubscriptionID)
	}
	if err != nil {
		return nil, err
	}
	return s.save(ctx, p, companyID, current.ID, snap, current)
}

// Resume withdraws a cancellation scheduled for the end of the period.
func (s *SubscriptionService) Resume(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.Subscription, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if !current.CancelAtPeriodEnd {
		return nil, &validation.ValidationError{Fields: map[string]string{
			"subscription": "no cancellation is scheduled"}}
	}
	snap, err := s.gateway.SetCancelAtPeriodEnd(ctx, current.StripeSubscriptionID, false)
	if err != nil {
		return nil, err
	}
	return s.save(ctx, p, companyID, current.ID, snap, current)
}

// ---------- scheduled changes ----------

// ScheduleChange switches the company to the given plan at the next
// renewal ("from next period: yearly"; "drop Support from next month").
// Nothing is charged or credited now: at the renewal Stripe simply bills
// the new plan. Scheduling again replaces the previous scheduled plan.
func (s *SubscriptionService) ScheduleChange(ctx context.Context, p auth.Principal, companyID uuid.UUID, in ChangeSubscriptionInput) (*model.Subscription, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	prices, err := s.validatePrices(ctx, in.PriceIDs)
	if err != nil {
		return nil, err
	}
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil {
		return nil, err
	}
	switch {
	case current.Status != model.SubscriptionActive && current.Status != model.SubscriptionTrialing:
		return nil, &validation.ValidationError{Fields: map[string]string{
			"subscription": "only a paid-up subscription can schedule a change"}}
	case current.CancelAtPeriodEnd:
		return nil, &validation.ValidationError{Fields: map[string]string{
			"subscription": "the subscription ends at the renewal; resume it before scheduling a change"}}
	}

	next, err := s.quantities(ctx, companyID, prices)
	if err != nil {
		return nil, err
	}
	if len(diffItems(current.Items, next)) == 0 {
		return nil, &validation.ValidationError{Fields: map[string]string{"price_ids": "this is already the current plan"}}
	}

	in2 := model.StripeScheduleChange{
		SubscriptionID: current.StripeSubscriptionID,
		CurrentItems:   stripeItems(current.Items),
		NextItems:      next,
		NextInterval:   prices[0].Interval,
	}
	if current.StripeScheduleID != nil {
		in2.ScheduleID = *current.StripeScheduleID
	}
	snap, err := s.gateway.ScheduleChange(ctx, in2)
	if err != nil {
		return nil, err
	}
	return s.save(ctx, p, companyID, current.ID, snap, current)
}

// CancelScheduledChange drops the plan change scheduled for the renewal:
// the subscription renews with its current plan.
func (s *SubscriptionService) CancelScheduledChange(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.Subscription, error) {
	if !canAccessCompany(p, companyID) {
		return nil, model.ErrNotFound
	}
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if current.StripeScheduleID == nil || current.ScheduledChangeAt == nil {
		return nil, &validation.ValidationError{Fields: map[string]string{"subscription": "no change is scheduled"}}
	}
	snap, err := s.gateway.ReleaseSchedule(ctx, *current.StripeScheduleID, current.StripeSubscriptionID)
	if err != nil {
		return nil, err
	}
	return s.save(ctx, p, companyID, current.ID, snap, current)
}

// refuseIfChangeScheduled: while a change waits for the renewal, other
// changes must not compete with it. The user cancels it first.
func refuseIfChangeScheduled(current *model.Subscription) error {
	if current.ScheduledChangeAt == nil {
		return nil
	}
	return &validation.ValidationError{Fields: map[string]string{
		"subscription": "a plan change is scheduled for " + current.ScheduledChangeAt.Format("2 Jan 2006") +
			"; cancel it first"}}
}

// releaseIdleSchedule detaches a schedule that has nothing left to do (its
// last phase is running), so the subscription can be changed directly.
func (s *SubscriptionService) releaseIdleSchedule(ctx context.Context, p auth.Principal, current *model.Subscription) (*model.Subscription, error) {
	if current.StripeScheduleID == nil || current.ScheduledChangeAt != nil {
		return current, nil
	}
	snap, err := s.gateway.ReleaseSchedule(ctx, *current.StripeScheduleID, current.StripeSubscriptionID)
	if err != nil {
		return nil, err
	}
	return s.save(ctx, p, current.CompanyID, current.ID, snap, current)
}

// stripeItems is the plan of items as Stripe prices and quantities.
func stripeItems(items []model.SubscriptionItem) []model.StripeSubscriptionItem {
	out := make([]model.StripeSubscriptionItem, 0, len(items))
	for _, it := range items {
		out = append(out, model.StripeSubscriptionItem{StripePriceID: it.Price.StripePriceID, Quantity: it.Quantity})
	}
	return out
}

// ---------- truck count ----------

// SyncTruckQuantity makes the company's per-truck items match its active
// truck count. The difference is prorated onto the next invoice (no charge
// now). Called after every fleet change, and hourly for every company
// (ReconcileTruckQuantities) in case a call to Stripe failed.
func (s *SubscriptionService) SyncTruckQuantity(ctx context.Context, companyID uuid.UUID) (changed bool, err error) {
	current, err := s.store.GetCurrent(ctx, companyID)
	if errors.Is(err, model.ErrNotFound) {
		return false, nil // no subscription: nothing to bill
	}
	if err != nil {
		return false, err
	}
	switch current.Status {
	case model.SubscriptionActive, model.SubscriptionTrialing, model.SubscriptionPastDue:
	default:
		return false, nil // not running (unpaid first invoice, suspended, ...)
	}

	trucks, err := s.trucks.CountActive(ctx, companyID)
	if err != nil {
		return false, err
	}

	// The current plan...
	var items []model.StripeItemChange
	for _, it := range current.Items {
		if it.Price.BillingUnit == model.BillingUnitTruck && it.Quantity != trucks {
			items = append(items, model.StripeItemChange{ID: it.StripeSubscriptionItemID, Quantity: trucks})
		}
	}
	// ...and the plan scheduled for the renewal: Stripe updates only the
	// current phase, so without this the fleet would "shrink back" then.
	nextNeedsUpdate := false
	next := make([]model.StripeSubscriptionItem, 0, len(current.ScheduledItems))
	for _, it := range current.ScheduledItems {
		qty := it.Quantity
		if it.Price.BillingUnit == model.BillingUnitTruck && qty != trucks {
			qty, nextNeedsUpdate = trucks, true
		}
		next = append(next, model.StripeSubscriptionItem{StripePriceID: it.Price.StripePriceID, Quantity: qty})
	}
	if len(items) == 0 && !nextNeedsUpdate {
		return false, nil
	}

	var snap *model.StripeSubscription
	if len(items) > 0 {
		if snap, err = s.gateway.SetQuantities(ctx, current.StripeSubscriptionID, items); err != nil {
			return false, err
		}
	}
	if nextNeedsUpdate {
		currentItems := stripeItems(current.Items)
		for i := range currentItems {
			if current.Items[i].Price.BillingUnit == model.BillingUnitTruck {
				currentItems[i].Quantity = trucks
			}
		}
		if snap, err = s.gateway.ScheduleChange(ctx, model.StripeScheduleChange{
			SubscriptionID: current.StripeSubscriptionID, ScheduleID: *current.StripeScheduleID,
			CurrentItems: currentItems, NextItems: next, NextInterval: current.ScheduledItems[0].Price.Interval,
		}); err != nil {
			return false, err
		}
	}
	if _, err := s.save(ctx, auth.SystemPrincipal(), companyID, current.ID, snap, current); err != nil {
		return false, err
	}
	return true, nil
}

// ReconcileTruckQuantities runs SyncTruckQuantity for every running
// subscription: the safety net behind the immediate sync. One failing
// company does not stop the others; their errors are returned together.
func (s *SubscriptionService) ReconcileTruckQuantities(ctx context.Context) (updated int, err error) {
	const pageSize = 100
	var errs []error
	for _, status := range []string{model.SubscriptionActive, model.SubscriptionTrialing, model.SubscriptionPastDue} {
		for offset := 0; ; offset += pageSize {
			subs, _, err := s.store.List(ctx, model.SubscriptionFilter{Status: status}, pageSize, offset)
			if err != nil {
				return updated, err
			}
			for _, sub := range subs {
				changed, err := s.SyncTruckQuantity(ctx, sub.CompanyID)
				if err != nil {
					errs = append(errs, fmt.Errorf("company %s: %w", sub.CompanyID, err))
					continue
				}
				if changed {
					updated++
				}
			}
			if len(subs) < pageSize {
				break
			}
		}
	}
	return updated, errors.Join(errs...)
}

// List returns subscriptions, newest first, history included. Company
// users see only their own company's.
func (s *SubscriptionService) List(ctx context.Context, p auth.Principal, params SubscriptionListParams) (*SubscriptionPage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})
	f := model.SubscriptionFilter{CompanyID: params.CompanyID, Status: params.Status}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID
	}
	items, total, err := s.store.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &SubscriptionPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}

// ---------- helpers ----------

// validatePrices checks the requested prices can be subscribed to together.
func (s *SubscriptionService) validatePrices(ctx context.Context, ids []uuid.UUID) ([]model.CatalogPrice, error) {
	var v validation.Validator
	v.Check(len(ids) >= 1, "price_ids", "choose at least one price")
	v.Check(len(ids) <= subscriptionMaxItems, "price_ids", "at most 20 prices")
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		v.Check(!seen[id], "price_ids", "each price at most once")
		seen[id] = true
	}
	if err := v.Err(); err != nil {
		return nil, err
	}

	found, err := s.prices.GetCatalogPrices(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]model.CatalogPrice, len(found))
	for _, cp := range found {
		byID[cp.ID] = cp
	}

	ordered := make([]model.CatalogPrice, 0, len(ids))
	products := map[uuid.UUID]bool{}
	for i, id := range ids {
		field := "price_ids[" + strconv.Itoa(i) + "]"
		cp, ok := byID[id]
		// Archived looks the same as missing: it cannot be chosen.
		if !ok || !cp.IsActive || !cp.ProductActive {
			v.Check(false, field, "price not found or archived")
			continue
		}
		v.Check(!products[cp.ProductID], field, "only one price per product")
		products[cp.ProductID] = true
		ordered = append(ordered, cp)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	// A Stripe rule: one subscription, one billing interval.
	for _, cp := range ordered[1:] {
		if cp.Interval != ordered[0].Interval {
			return nil, &validation.ValidationError{Fields: map[string]string{
				"price_ids": "all prices must have the same interval (all monthly or all yearly)"}}
		}
	}
	return ordered, nil
}

// quantities: 1 for flat prices, the active truck count for per-truck ones.
func (s *SubscriptionService) quantities(ctx context.Context, companyID uuid.UUID, prices []model.CatalogPrice) ([]model.StripeSubscriptionItem, error) {
	trucks := -1 // counted once, only if needed
	items := make([]model.StripeSubscriptionItem, 0, len(prices))
	for _, cp := range prices {
		qty := 1
		if cp.BillingUnit == model.BillingUnitTruck {
			if trucks < 0 {
				n, err := s.trucks.CountActive(ctx, companyID)
				if err != nil {
					return nil, err
				}
				trucks = n
			}
			qty = trucks
		}
		items = append(items, model.StripeSubscriptionItem{StripePriceID: cp.StripePriceID, Quantity: qty})
	}
	return items, nil
}

// defaultCard returns the company's default card id. Paying first needs a
// card to pay with: without one, a validation error says what to do.
func (s *SubscriptionService) defaultCard(ctx context.Context, p auth.Principal, companyID uuid.UUID) (string, error) {
	cards, err := s.customers.ListPaymentMethods(ctx, p, companyID)
	if err != nil {
		return "", err
	}
	for _, c := range cards {
		if c.IsDefault {
			return c.ID, nil
		}
	}
	return "", &validation.ValidationError{Fields: map[string]string{
		"payment_method": "add a card and make it the default first"}}
}

// save rebuilds our copy from what Stripe returned and records the change.
// before is our previous copy (nil when the subscription is new).
func (s *SubscriptionService) save(ctx context.Context, p auth.Principal, companyID, id uuid.UUID,
	snap *model.StripeSubscription, before *model.Subscription) (*model.Subscription, error) {
	sub, err := s.fromStripe(ctx, companyID, id, snap)
	if err != nil {
		return nil, err
	}

	var saved *model.Subscription
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.store.Upsert(ctx, *sub); err != nil {
			return err
		}
		action, ch := "subscription.create", subscriptionChanges(model.Subscription{}, *saved)
		if before != nil {
			action, ch = "subscription.sync", subscriptionChanges(*before, *saved)
		}
		if len(ch) > 0 { // nothing new from Stripe = nothing to record
			if err := s.audit.Record(ctx, p, model.AuditEntry{
				Action: action, Entity: model.AuditSubscription, EntityID: saved.ID.String(),
				CompanyID: &saved.CompanyID, Changes: ch,
			}); err != nil {
				return err
			}
		}
		return s.updateBillingStatus(ctx, p, saved.CompanyID)
	})
	return saved, err
}

// updateBillingStatus derives the company's billing status from its
// CURRENT subscription (not from the one just synced: a late event about
// an old, ended subscription must not block a company that has a new one).
func (s *SubscriptionService) updateBillingStatus(ctx context.Context, p auth.Principal, companyID uuid.UUID) error {
	current, err := s.store.GetCurrent(ctx, companyID)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return err
	}
	status := model.BillingStatusFor(current) // current is nil when not found
	old, err := s.companies.SetBillingStatus(ctx, companyID, status)
	if err != nil {
		return err
	}
	if old == status {
		return nil
	}
	return s.audit.Record(ctx, p, model.AuditEntry{
		Action: "company.billing_status", Entity: model.AuditCompany, EntityID: companyID.String(),
		CompanyID: &companyID, Changes: model.AuditChanges{"billing_status": {Old: old, New: status}},
	})
}

// fromStripe maps Stripe's subscription onto our model: Stripe price ids
// become our prices.
func (s *SubscriptionService) fromStripe(ctx context.Context, companyID, id uuid.UUID, snap *model.StripeSubscription) (*model.Subscription, error) {
	stripeIDs := make([]string, 0, len(snap.Items))
	for _, it := range snap.Items {
		stripeIDs = append(stripeIDs, it.StripePriceID)
	}
	if snap.Schedule != nil {
		for _, it := range snap.Schedule.NextItems {
			stripeIDs = append(stripeIDs, it.StripePriceID)
		}
	}
	found, err := s.prices.GetCatalogPricesByStripeIDs(ctx, stripeIDs)
	if err != nil {
		return nil, err
	}
	byStripe := make(map[string]model.CatalogPrice, len(found))
	for _, cp := range found {
		byStripe[cp.StripePriceID] = cp
	}

	sub := &model.Subscription{
		ID: id, CompanyID: companyID, StripeSubscriptionID: snap.ID, Status: snap.Status,
		CancelAtPeriodEnd: snap.CancelAtPeriodEnd, CanceledAt: snap.CanceledAt,
		Items: make([]model.SubscriptionItem, 0, len(snap.Items)),
	}
	for _, it := range snap.Items {
		cp, ok := byStripe[it.StripePriceID]
		if !ok {
			// Added in the Stripe Dashboard, not through our API.
			return nil, fmt.Errorf("subscription %s uses price %s, which is not in our catalog", snap.ID, it.StripePriceID)
		}
		sub.Interval = cp.Interval
		// All items share one interval, so their periods are the same.
		sub.CurrentPeriodStart, sub.CurrentPeriodEnd = it.PeriodStart, it.PeriodEnd
		sub.Items = append(sub.Items, model.SubscriptionItem{
			StripeSubscriptionItemID: it.ID, Price: cp.Price, ProductName: cp.ProductName, Quantity: it.Quantity,
		})
	}

	if sch := snap.Schedule; sch != nil {
		sub.StripeScheduleID = &sch.ID
		sub.ScheduledChangeAt = sch.NextAt
		for _, it := range sch.NextItems {
			cp, ok := byStripe[it.StripePriceID]
			if !ok {
				return nil, fmt.Errorf("schedule %s uses price %s, which is not in our catalog", sch.ID, it.StripePriceID)
			}
			sub.ScheduledItems = append(sub.ScheduledItems, model.SubscriptionItem{
				Price: cp.Price, ProductName: cp.ProductName, Quantity: it.Quantity,
			})
		}
	}
	return sub, nil
}

// paymentDue: the open invoice the browser still has to pay, if any: the
// first one, or the one of a plan change waiting for payment.
func paymentDue(snap *model.StripeSubscription, card string) *PaymentDue {
	inv := snap.LatestInvoice
	waiting := snap.Status == model.SubscriptionIncomplete || snap.PendingUpdate
	if !waiting || inv == nil || inv.Status != "open" ||
		inv.AmountDue == 0 || inv.ClientSecret == "" {
		return nil
	}
	return &PaymentDue{InvoiceID: inv.ID, AmountDue: inv.AmountDue, ClientSecret: inv.ClientSecret, PaymentMethodID: card}
}

// canAccessCompany: company users only their own; platform users any.
func canAccessCompany(p auth.Principal, companyID uuid.UUID) bool {
	return p.IsPlatformUser() || *p.CompanyID == companyID
}

func isValidation(err error) bool {
	var vErr *validation.ValidationError
	return errors.As(err, &vErr)
}

func subscriptionChanges(before, after model.Subscription) model.AuditChanges {
	ch := model.AuditChanges{}
	ch.Set("status", before.Status, after.Status)
	ch.Set("stripe_subscription_id", before.StripeSubscriptionID, after.StripeSubscriptionID)
	ch.Set("interval", before.Interval, after.Interval)
	ch.Set("cancel_at_period_end", before.CancelAtPeriodEnd, after.CancelAtPeriodEnd)
	ch.Set("items", describeItems(before.Items), describeItems(after.Items))
	ch.Set("scheduled_items", describeItems(before.ScheduledItems), describeItems(after.ScheduledItems))
	ch.Set("scheduled_change_at", before.ScheduledChangeAt, after.ScheduledChangeAt)
	return ch
}

// describeItems is how items appear in the audit log: "ELD Fleet plan
// (Monthly) × 12".
func describeItems(items []model.SubscriptionItem) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		name := it.ProductName
		if it.Price.Nickname != "" {
			name += " (" + it.Price.Nickname + ")"
		}
		out = append(out, fmt.Sprintf("%s × %d", name, it.Quantity))
	}
	return out
}
