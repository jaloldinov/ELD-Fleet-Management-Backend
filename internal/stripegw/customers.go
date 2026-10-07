package stripegw

import (
	"context"
	"errors"

	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// MetaCompanyID links a Stripe Customer back to our company.
const MetaCompanyID = "eld_company_id"

// Customers manages Stripe Customers (one per company) and their saved
// cards.
type Customers struct {
	c *stripe.Client
}

func NewCustomers(c *stripe.Client) *Customers {
	return &Customers{c: c}
}

// CreateCustomer creates the Stripe Customer of company c and returns its
// id (cus_...).
//
// The idempotency key is the company id: two requests that race to create
// the first customer of a company get the SAME customer back from Stripe
// (keys are remembered for 24 hours; after that the id is long saved).
func (g *Customers) CreateCustomer(ctx context.Context, c model.Company) (string, error) {
	params := &stripe.CustomerCreateParams{
		Name:        stripe.String(c.Name),
		Description: stripe.String("USDOT " + c.DOTNumber),
		Metadata:    map[string]string{MetaCompanyID: c.ID.String()},
	}
	params.SetIdempotencyKey("customer-create-" + c.ID.String())

	created, err := g.c.V1Customers.Create(ctx, params)
	if err != nil {
		return "", wrap("create customer", err)
	}
	return created.ID, nil
}

// CreateSetupIntent lets the browser save a card for the customer, and
// returns the SetupIntent's client secret: a one-time permission that
// Stripe.js uses to send the card directly to Stripe.
//
// usage=off_session: the card will be charged later without the user
// present (monthly invoices), so the bank may ask for 3-D Secure now, once,
// instead of at every renewal.
func (g *Customers) CreateSetupIntent(ctx context.Context, customerID string) (string, error) {
	params := &stripe.SetupIntentCreateParams{
		Customer:                  stripe.String(customerID),
		Usage:                     stripe.String(string(stripe.SetupIntentUsageOffSession)),
		AllowedPaymentMethodTypes: []*string{stripe.String("card")},
	}
	si, err := g.c.V1SetupIntents.Create(ctx, params)
	if err != nil {
		return "", wrap("create setup intent", err)
	}
	return si.ClientSecret, nil
}

// ListPaymentMethods returns the customer's saved cards, the default one
// marked.
func (g *Customers) ListPaymentMethods(ctx context.Context, customerID string) ([]model.PaymentMethod, error) {
	cust, err := g.c.V1Customers.Retrieve(ctx, customerID, nil)
	if err != nil {
		return nil, wrap("retrieve customer", err)
	}
	var defaultID string
	if cust.InvoiceSettings != nil && cust.InvoiceSettings.DefaultPaymentMethod != nil {
		defaultID = cust.InvoiceSettings.DefaultPaymentMethod.ID
	}

	params := &stripe.PaymentMethodListParams{
		Customer: stripe.String(customerID),
		Type:     stripe.String(string(stripe.PaymentMethodTypeCard)),
	}
	out := []model.PaymentMethod{}
	// All() follows Stripe's pages (100 per call) until the end.
	for pm, err := range g.c.V1PaymentMethods.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, wrap("list payment methods", err)
		}
		m := toPaymentMethod(pm)
		m.IsDefault = pm.ID == defaultID
		out = append(out, m)
	}
	return out, nil
}

// GetPaymentMethod returns one card; model.ErrNotFound if Stripe does not
// know the id. The caller checks whom it belongs to.
func (g *Customers) GetPaymentMethod(ctx context.Context, id string) (*model.PaymentMethod, error) {
	pm, err := g.c.V1PaymentMethods.Retrieve(ctx, id, nil)
	if err != nil {
		if isResourceMissing(err) {
			return nil, model.ErrNotFound
		}
		return nil, wrap("retrieve payment method", err)
	}
	m := toPaymentMethod(pm)
	return &m, nil
}

// SetDefaultPaymentMethod makes the card the one charged for invoices.
func (g *Customers) SetDefaultPaymentMethod(ctx context.Context, customerID, paymentMethodID string) error {
	params := &stripe.CustomerUpdateParams{
		InvoiceSettings: &stripe.CustomerUpdateInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(paymentMethodID),
		},
	}
	if _, err := g.c.V1Customers.Update(ctx, customerID, params); err != nil {
		return wrap("set default payment method", err)
	}
	return nil
}

// DetachPaymentMethod removes the card from its customer. It cannot be
// used again afterwards.
func (g *Customers) DetachPaymentMethod(ctx context.Context, id string) error {
	if _, err := g.c.V1PaymentMethods.Detach(ctx, id, nil); err != nil {
		return wrap("detach payment method", err)
	}
	return nil
}

func toPaymentMethod(pm *stripe.PaymentMethod) model.PaymentMethod {
	m := model.PaymentMethod{ID: pm.ID}
	if pm.Customer != nil {
		m.CustomerID = pm.Customer.ID
	}
	if pm.Card != nil {
		m.Brand = string(pm.Card.Brand)
		m.Last4 = pm.Card.Last4
		m.ExpMonth = int(pm.Card.ExpMonth)
		m.ExpYear = int(pm.Card.ExpYear)
	}
	return m
}

// isResourceMissing: Stripe answered "no such object" (404).
func isResourceMissing(err error) bool {
	var sErr *stripe.Error
	return errors.As(err, &sErr) && sErr.Code == stripe.ErrorCodeResourceMissing
}
