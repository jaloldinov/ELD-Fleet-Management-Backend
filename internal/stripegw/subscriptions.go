package stripegw

import (
	"context"
	"time"

	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// MetaSubscriptionID links a Stripe subscription back to our row.
const MetaSubscriptionID = "eld_subscription_id"

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
//   - save_default_payment_method=on_subscription: the card that pays the
//     first invoice also pays every renewal.
func (g *Subscriptions) CreateSubscription(ctx context.Context, in model.StripeSubscriptionCreate) (*model.StripeSubscription, error) {
	items := make([]*stripe.SubscriptionCreateItemParams, 0, len(in.Items))
	for _, it := range in.Items {
		items = append(items, &stripe.SubscriptionCreateItemParams{
			Price:    stripe.String(it.StripePriceID),
			Quantity: stripe.Int64(int64(it.Quantity)),
		})
	}
	params := &stripe.SubscriptionCreateParams{
		Customer:             stripe.String(in.CustomerID),
		Items:                items,
		DefaultPaymentMethod: stripe.String(in.PaymentMethodID),
		PaymentBehavior:      stripe.String("default_incomplete"),
		PaymentSettings: &stripe.SubscriptionCreatePaymentSettingsParams{
			SaveDefaultPaymentMethod: stripe.String("on_subscription"),
		},
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

func toStripeSubscription(sub *stripe.Subscription) *model.StripeSubscription {
	out := &model.StripeSubscription{
		ID:                sub.ID,
		Status:            string(sub.Status),
		CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
		CanceledAt:        unixTime(sub.CanceledAt),
		Items:             []model.StripeSubscriptionItem{},
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
