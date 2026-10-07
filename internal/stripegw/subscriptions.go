package stripegw

import (
	"context"
	"time"

	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// MetaSubscriptionID links a Stripe subscription back to our row.
const MetaSubscriptionID = model.StripeMetaSubscriptionID

// Stripe returns the first invoice's payment secret only when asked to
// "expand" it (include the nested object instead of just its id).
const expandConfirmationSecret = "latest_invoice.confirmation_secret"

// Subscriptions starts and reads Stripe subscriptions.
type Subscriptions struct {
	c *stripe.Client
}

func NewSubscriptions(c *stripe.Client) *Subscriptions {
	return &Subscriptions{c: c}
}

// CreateSubscription starts a subscription that is NOT paid yet: "pay first,
// then use".
//
//   - payment_behavior=default_incomplete: Stripe creates the subscription
//     (status incomplete) and its first invoice, but does not charge. The
//     browser confirms the payment with the returned client secret, so the
//     user is present if the bank asks for 3-D Secure. Unpaid, it expires
//     after 23 hours (incomplete_expired).
//   - No card is stored ON the subscription: renewals charge the customer's
//     default card (invoice_settings.default_payment_method), so "Make
//     default" in our API also changes which card pays the next renewal.
func (g *Subscriptions) CreateSubscription(ctx context.Context, in model.StripeSubscriptionCreate) (*model.StripeSubscription, error) {
	items := make([]*stripe.SubscriptionCreateItemParams, 0, len(in.Items))
	for _, it := range in.Items {
		items = append(items, &stripe.SubscriptionCreateItemParams{
			Price:    stripe.String(it.StripePriceID),
			Quantity: stripe.Int64(int64(it.Quantity)),
		})
	}
	params := &stripe.SubscriptionCreateParams{
		Customer:        stripe.String(in.CustomerID),
		Items:           items,
		PaymentBehavior: stripe.String("default_incomplete"),
		Metadata: map[string]string{
			MetaSubscriptionID: in.ID.String(),
			MetaCompanyID:      in.CompanyID.String(),
		},
	}
	params.AddExpand(expandConfirmationSecret)
	params.SetIdempotencyKey("subscription-create-" + in.ID.String())

	sub, err := g.c.V1Subscriptions.Create(ctx, params)
	if err != nil {
		return nil, wrap("create subscription", err)
	}
	return toStripeSubscription(sub), nil
}

// GetSubscription reads a subscription as Stripe sees it now.
func (g *Subscriptions) GetSubscription(ctx context.Context, id string) (*model.StripeSubscription, error) {
	params := &stripe.SubscriptionRetrieveParams{}
	params.AddExpand(expandConfirmationSecret)
	sub, err := g.c.V1Subscriptions.Retrieve(ctx, id, params)
	if err != nil {
		if isResourceMissing(err) {
			return nil, model.ErrNotFound
		}
		return nil, wrap("retrieve subscription", err)
	}
	return toStripeSubscription(sub), nil
}

// PreviewChange asks Stripe what changing the subscription's items would
// cost right now: Stripe builds the invoice the change would create (with
// its proration lines) without creating anything.
//
// Stripe's preview also lists the PENDING invoice items (e.g. prorations
// from trucks added this month, which wait for the next renewal invoice),
// but the real change does not charge them now. So the quote counts only
// the lines the change itself creates, then applies the customer's credit.
func (g *Subscriptions) PreviewChange(ctx context.Context, subscriptionID, customerID string, items []model.StripeItemChange) (*model.ProrationPreview, error) {
	pending, err := g.pendingInvoiceItems(ctx, customerID)
	if err != nil {
		return nil, err
	}

	prorationDate := time.Now().Unix()
	details := &stripe.InvoiceCreatePreviewSubscriptionDetailsParams{
		ProrationBehavior: stripe.String("always_invoice"),
		ProrationDate:     stripe.Int64(prorationDate),
	}
	for _, it := range items {
		details.Items = append(details.Items, previewItem(it))
	}
	inv, err := g.c.V1Invoices.CreatePreview(ctx, &stripe.InvoiceCreatePreviewParams{
		Customer:            stripe.String(customerID),
		Subscription:        stripe.String(subscriptionID),
		SubscriptionDetails: details,
	})
	if err != nil {
		return nil, wrap("preview subscription change", err)
	}

	out := &model.ProrationPreview{ProrationDate: prorationDate}
	if inv.Lines != nil {
		for _, l := range inv.Lines.Data {
			if pending[invoiceItemID(l)] {
				continue // already waiting for the renewal; not part of this change
			}
			out.Lines = append(out.Lines, model.PreviewLine{Description: l.Description, Amount: l.Amount, Proration: isProration(l)})
			out.Total += l.Amount
		}
	}
	// The customer's balance (negative = credit) pays first.
	out.AmountDue = max(0, out.Total+inv.StartingBalance)
	return out, nil
}

// pendingInvoiceItems returns the ids of the customer's invoice items that
// wait for the next invoice.
func (g *Subscriptions) pendingInvoiceItems(ctx context.Context, customerID string) (map[string]bool, error) {
	params := &stripe.InvoiceItemListParams{Customer: stripe.String(customerID), Pending: stripe.Bool(true)}
	out := map[string]bool{}
	for ii, err := range g.c.V1InvoiceItems.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, wrap("list pending invoice items", err)
		}
		out[ii.ID] = true
	}
	return out, nil
}

// invoiceItemID is the invoice item (ii_...) a line comes from, if any.
func invoiceItemID(l *stripe.InvoiceLineItem) string {
	if l.Parent == nil {
		return ""
	}
	if d := l.Parent.SubscriptionItemDetails; d != nil {
		return d.InvoiceItem
	}
	if d := l.Parent.InvoiceItemDetails; d != nil {
		return d.InvoiceItem
	}
	return ""
}

// ChangeItems applies a plan change.
//
//   - proration_behavior=always_invoice: the difference is invoiced (and
//     charged) at once; a negative difference becomes customer credit.
//   - payment_behavior=pending_if_incomplete: pay first, for upgrades too.
//     If the charge fails or needs 3-D Secure, Stripe keeps the OLD items
//     and holds the change as a "pending update" until the invoice is
//     paid (or for 23 hours).
//   - proration_date: the moment the preview was computed for, so the
//     charge equals what the user saw.
func (g *Subscriptions) ChangeItems(ctx context.Context, subscriptionID string, items []model.StripeItemChange, prorationDate int64) (*model.StripeSubscription, error) {
	params := &stripe.SubscriptionUpdateParams{
		ProrationBehavior: stripe.String("always_invoice"),
		PaymentBehavior:   stripe.String("pending_if_incomplete"),
		ProrationDate:     stripe.Int64(prorationDate),
	}
	for _, it := range items {
		params.Items = append(params.Items, updateItem(it))
	}
	params.AddExpand(expandConfirmationSecret)

	sub, err := g.c.V1Subscriptions.Update(ctx, subscriptionID, params)
	if err != nil {
		return nil, wrap("change subscription", err)
	}
	return toStripeSubscription(sub), nil
}

// SetQuantities changes item quantities (the truck count) without charging
// now: create_prorations puts the prorated difference on the NEXT invoice,
// so adding three trucks does not mean three card charges.
func (g *Subscriptions) SetQuantities(ctx context.Context, subscriptionID string, items []model.StripeItemChange) (*model.StripeSubscription, error) {
	params := &stripe.SubscriptionUpdateParams{ProrationBehavior: stripe.String("create_prorations")}
	for _, it := range items {
		params.Items = append(params.Items, updateItem(it))
	}
	params.AddExpand(expandConfirmationSecret)
	sub, err := g.c.V1Subscriptions.Update(ctx, subscriptionID, params)
	if err != nil {
		return nil, wrap("update subscription quantities", err)
	}
	return toStripeSubscription(sub), nil
}

// SetCancelAtPeriodEnd schedules (true) or withdraws (false) the end of
// the subscription at the end of the paid period. Nothing is charged or
// refunded: the company keeps what it paid for.
func (g *Subscriptions) SetCancelAtPeriodEnd(ctx context.Context, subscriptionID string, cancel bool) (*model.StripeSubscription, error) {
	params := &stripe.SubscriptionUpdateParams{CancelAtPeriodEnd: stripe.Bool(cancel)}
	params.AddExpand(expandConfirmationSecret)
	sub, err := g.c.V1Subscriptions.Update(ctx, subscriptionID, params)
	if err != nil {
		return nil, wrap("set cancel at period end", err)
	}
	return toStripeSubscription(sub), nil
}

// CancelNow ends the subscription immediately. The unused time is prorated
// and invoiced at once (invoice_now), which turns it into customer credit.
func (g *Subscriptions) CancelNow(ctx context.Context, subscriptionID string) (*model.StripeSubscription, error) {
	params := &stripe.SubscriptionCancelParams{Prorate: stripe.Bool(true), InvoiceNow: stripe.Bool(true)}
	params.AddExpand(expandConfirmationSecret)
	sub, err := g.c.V1Subscriptions.Cancel(ctx, subscriptionID, params)
	if err != nil {
		return nil, wrap("cancel subscription", err)
	}
	return toStripeSubscription(sub), nil
}

func previewItem(it model.StripeItemChange) *stripe.InvoiceCreatePreviewSubscriptionDetailsItemParams {
	p := &stripe.InvoiceCreatePreviewSubscriptionDetailsItemParams{}
	switch {
	case it.Deleted:
		p.ID, p.Deleted = stripe.String(it.ID), stripe.Bool(true)
	case it.ID != "":
		p.ID, p.Quantity = stripe.String(it.ID), stripe.Int64(int64(it.Quantity))
	default:
		p.Price, p.Quantity = stripe.String(it.StripePriceID), stripe.Int64(int64(it.Quantity))
	}
	return p
}

func updateItem(it model.StripeItemChange) *stripe.SubscriptionUpdateItemParams {
	p := &stripe.SubscriptionUpdateItemParams{}
	switch {
	case it.Deleted:
		p.ID, p.Deleted = stripe.String(it.ID), stripe.Bool(true)
	case it.ID != "":
		p.ID, p.Quantity = stripe.String(it.ID), stripe.Int64(int64(it.Quantity))
	default:
		p.Price, p.Quantity = stripe.String(it.StripePriceID), stripe.Int64(int64(it.Quantity))
	}
	return p
}

// isProration: the line is part of a change (credit for unused time, or
// charge for the rest of the period), not a regular period charge.
func isProration(l *stripe.InvoiceLineItem) bool {
	if l.Parent == nil {
		return false
	}
	if d := l.Parent.SubscriptionItemDetails; d != nil && d.Proration {
		return true
	}
	if d := l.Parent.InvoiceItemDetails; d != nil && d.Proration {
		return true
	}
	return false
}

// InvoiceSubscriptionID returns the id of the subscription an invoice
// belongs to; "" for an invoice that is not a subscription's (a one-off
// charge).
func (g *Subscriptions) InvoiceSubscriptionID(ctx context.Context, invoiceID string) (string, error) {
	inv, err := g.c.V1Invoices.Retrieve(ctx, invoiceID, nil)
	if err != nil {
		if isResourceMissing(err) {
			return "", model.ErrNotFound
		}
		return "", wrap("retrieve invoice", err)
	}
	if inv.Parent == nil || inv.Parent.SubscriptionDetails == nil || inv.Parent.SubscriptionDetails.Subscription == nil {
		return "", nil
	}
	return inv.Parent.SubscriptionDetails.Subscription.ID, nil
}

func toStripeSubscription(sub *stripe.Subscription) *model.StripeSubscription {
	out := &model.StripeSubscription{
		ID:                sub.ID,
		Metadata:          sub.Metadata,
		Status:            string(sub.Status),
		CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
		CanceledAt:        unixTime(sub.CanceledAt),
		Items:             []model.StripeSubscriptionItem{},
		PendingUpdate:     sub.PendingUpdate != nil,
	}
	if sub.Customer != nil {
		out.CustomerID = sub.Customer.ID
	}
	if sub.Items != nil {
		for _, it := range sub.Items.Data {
			si := model.StripeSubscriptionItem{
				ID:          it.ID,
				Quantity:    int(it.Quantity),
				PeriodStart: unixTime(it.CurrentPeriodStart),
				PeriodEnd:   unixTime(it.CurrentPeriodEnd),
			}
			if it.Price != nil {
				si.StripePriceID = it.Price.ID
			}
			out.Items = append(out.Items, si)
		}
	}
	if inv := sub.LatestInvoice; inv != nil {
		out.LatestInvoice = &model.StripeInvoice{ID: inv.ID, Status: string(inv.Status), AmountDue: inv.AmountDue}
		if inv.ConfirmationSecret != nil {
			out.LatestInvoice.ClientSecret = inv.ConfirmationSecret.ClientSecret
		}
	}
	return out
}

// unixTime turns Stripe's Unix seconds into a time; 0 (= not set) into nil.
func unixTime(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}
