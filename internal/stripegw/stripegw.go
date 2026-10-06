// Package stripegw is the gateway to Stripe: it turns our domain types into
// Stripe API calls, like the repository turns them into SQL.
//
// Rules for this layer (the same as for repository):
//   - no business rules (the service decides WHAT to do; this says HOW)
//   - no HTTP knowledge
//   - Stripe errors are wrapped in model.ErrPaymentProvider
//
// Our ids travel to Stripe as metadata (eld_product_id, eld_price_id), so
// anything seen on Stripe's side (the Dashboard, a webhook) can be traced
// back to our rows.
package stripegw

import (
	"fmt"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Metadata keys we set on Stripe objects.
const (
	MetaProductID   = "eld_product_id"
	MetaPriceID     = "eld_price_id"
	MetaBillingUnit = "eld_billing_unit"
)

// wrap marks err as a payment-provider failure while keeping the Stripe
// error inside (for logs and errors.As).
func wrap(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", model.ErrPaymentProvider, op, err)
}
