package stripegw

import (
	"fmt"

	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Webhooks checks that a webhook really comes from Stripe.
type Webhooks struct {
	secret string // whsec_...
}

func NewWebhooks(secret string) *Webhooks {
	return &Webhooks{secret: secret}
}

// Parse verifies the Stripe-Signature header of payload and returns the
// event. Anyone can send a request to our webhook URL; only Stripe knows
// the secret the signature is made with. The signature also covers a
// timestamp: events older than 5 minutes are refused, so a recorded request
// cannot be replayed later.
//
// The event's API version is not checked: we only read its type and the id
// of its object, then fetch the object itself from Stripe with our pinned
// version. A webhook is a doorbell, not a source of truth.
func (w *Webhooks) Parse(payload []byte, signatureHeader string) (*model.StripeEvent, error) {
	ev, err := stripe.ConstructEvent(payload, signatureHeader, w.secret, stripe.WithIgnoreAPIVersionMismatch())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", model.ErrWebhookSignature, err)
	}
	out := &model.StripeEvent{ID: ev.ID, Type: string(ev.Type), Livemode: ev.Livemode}
	if ev.Data != nil {
		out.ObjectID, _ = ev.Data.Object["id"].(string)
		out.ObjectType, _ = ev.Data.Object["object"].(string)
	}
	return out, nil
}
