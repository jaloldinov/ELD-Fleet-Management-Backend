package service

import (
	"context"
	"fmt"
	"regexp"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// CustomerCompanies is what CustomerService needs from the company store
// (repository.CompanyRepository).
type CustomerCompanies interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Company, error)
	SetStripeCustomerID(ctx context.Context, id uuid.UUID, customerID string) (linked string, err error)
}

// CustomerGateway is what CustomerService needs from Stripe
// (stripegw.Customers).
type CustomerGateway interface {
	CreateCustomer(ctx context.Context, c model.Company) (customerID string, err error)
	CreateSetupIntent(ctx context.Context, customerID string) (clientSecret string, err error)
	ListPaymentMethods(ctx context.Context, customerID string) ([]model.PaymentMethod, error)
	GetPaymentMethod(ctx context.Context, id string) (*model.PaymentMethod, error)
	SetDefaultPaymentMethod(ctx context.Context, customerID, paymentMethodID string) error
	DetachPaymentMethod(ctx context.Context, id string) error
}

// Stripe ids are opaque, but a malformed one can be refused without a call.
var paymentMethodIDRe = regexp.MustCompile(`^pm_[A-Za-z0-9_]{1,100}$`)

// SetupIntent is what the browser needs to show Stripe's card form.
type SetupIntent struct {
	ClientSecret   string // one-time permission to save a card for this customer
	PublishableKey string // pk_...: identifies our Stripe account to Stripe.js
}

// CustomerService links companies to Stripe Customers and manages their
// saved cards.
//
// Scope: company users act on their own company only (any other company
// id is "not found"); platform users on any company.
//
// Cards are entered in the browser and sent by Stripe.js straight to
// Stripe: our server never sees a card number, only ids and the last 4
// digits.
type CustomerService struct {
	companies      CustomerCompanies
	gateway        CustomerGateway
	audit          *Auditor
	publishableKey string
}

func NewCustomerService(companies CustomerCompanies, gateway CustomerGateway, audit *Auditor, publishableKey string) *CustomerService {
	return &CustomerService{companies: companies, gateway: gateway, audit: audit, publishableKey: publishableKey}
}

// EnsureCustomer returns the company's Stripe Customer id, creating the
// customer the first time. Used by every billing feature that needs one.
func (s *CustomerService) EnsureCustomer(ctx context.Context, p auth.Principal, companyID uuid.UUID) (string, error) {
	c, err := s.company(ctx, p, companyID)
	if err != nil {
		return "", err
	}
	return s.ensureCustomer(ctx, p, c)
}

func (s *CustomerService) ensureCustomer(ctx context.Context, p auth.Principal, c *model.Company) (string, error) {
	if c.StripeCustomerID != nil {
		return *c.StripeCustomerID, nil
	}

	created, err := s.gateway.CreateCustomer(ctx, *c)
	if err != nil {
		return "", err
	}

	var linked string
	err = s.audit.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		// If another request linked one first, keep theirs. Thanks to the
		// idempotency key it is the same customer anyway.
		if linked, err = s.companies.SetStripeCustomerID(ctx, c.ID, created); err != nil {
			return err
		}
		if linked != created {
			return nil // the other request already recorded it
		}
		return s.audit.Record(ctx, p, model.AuditEntry{
			Action: "company.billing_enable", Entity: model.AuditCompany, EntityID: c.ID.String(), CompanyID: &c.ID,
			Changes: model.AuditChanges{"stripe_customer_id": {Old: nil, New: created}},
		})
	})
	if err != nil {
		return "", fmt.Errorf("customer created in Stripe as %s but not linked here: %w", created, err)
	}
	return linked, nil
}

// CreateSetupIntent starts "add a card": the browser gets a one-time
// permission to send a card to Stripe for this company.
func (s *CustomerService) CreateSetupIntent(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*SetupIntent, error) {
	customerID, err := s.EnsureCustomer(ctx, p, companyID)
	if err != nil {
		return nil, err
	}
	secret, err := s.gateway.CreateSetupIntent(ctx, customerID)
	if err != nil {
		return nil, err
	}
	return &SetupIntent{ClientSecret: secret, PublishableKey: s.publishableKey}, nil
}

// ListPaymentMethods returns the company's saved cards. A company without
// a Stripe Customer simply has none: reading never creates anything.
func (s *CustomerService) ListPaymentMethods(ctx context.Context, p auth.Principal, companyID uuid.UUID) ([]model.PaymentMethod, error) {
	c, err := s.company(ctx, p, companyID)
	if err != nil {
		return nil, err
	}
	if c.StripeCustomerID == nil {
		return []model.PaymentMethod{}, nil
	}
	return s.gateway.ListPaymentMethods(ctx, *c.StripeCustomerID)
}

// SetDefaultPaymentMethod makes one of the company's cards the one charged
// for invoices.
func (s *CustomerService) SetDefaultPaymentMethod(ctx context.Context, p auth.Principal, companyID uuid.UUID, paymentMethodID string) error {
	c, pm, err := s.ownedPaymentMethod(ctx, p, companyID, paymentMethodID)
	if err != nil {
		return err
	}
	if err := s.gateway.SetDefaultPaymentMethod(ctx, *c.StripeCustomerID, pm.ID); err != nil {
		return err
	}
	return s.recordCard(ctx, p, c, "company.payment_method_default",
		model.AuditChanges{"default_payment_method": {Old: nil, New: describeCard(pm)}})
}

// RemovePaymentMethod detaches a card from the company. It cannot be used
// again; adding it back means entering it again.
func (s *CustomerService) RemovePaymentMethod(ctx context.Context, p auth.Principal, companyID uuid.UUID, paymentMethodID string) error {
	c, pm, err := s.ownedPaymentMethod(ctx, p, companyID, paymentMethodID)
	if err != nil {
		return err
	}
	if err := s.gateway.DetachPaymentMethod(ctx, pm.ID); err != nil {
		return err
	}
	return s.recordCard(ctx, p, c, "company.payment_method_remove",
		model.AuditChanges{"payment_method": {Old: describeCard(pm), New: nil}})
}

// ownedPaymentMethod loads the company and a card that belongs to it.
//
// The ownership check matters: Stripe would happily detach ANY card of our
// account given its id. A card of another company, a made-up id and a card
// of a company without billing all look the same: not found.
func (s *CustomerService) ownedPaymentMethod(ctx context.Context, p auth.Principal, companyID uuid.UUID, paymentMethodID string) (*model.Company, *model.PaymentMethod, error) {
	c, err := s.company(ctx, p, companyID)
	if err != nil {
		return nil, nil, err
	}
	if c.StripeCustomerID == nil || !paymentMethodIDRe.MatchString(paymentMethodID) {
		return nil, nil, model.ErrNotFound
	}
	pm, err := s.gateway.GetPaymentMethod(ctx, paymentMethodID)
	if err != nil {
		return nil, nil, err
	}
	if pm.CustomerID != *c.StripeCustomerID {
		return nil, nil, model.ErrNotFound
	}
	return c, pm, nil
}

// company loads the company the caller may act on.
func (s *CustomerService) company(ctx context.Context, p auth.Principal, companyID uuid.UUID) (*model.Company, error) {
	if !p.IsPlatformUser() && *p.CompanyID != companyID {
		return nil, model.ErrNotFound // another tenant's company "does not exist"
	}
	return s.companies.GetByID(ctx, companyID)
}

func (s *CustomerService) recordCard(ctx context.Context, p auth.Principal, c *model.Company, action string, ch model.AuditChanges) error {
	return s.audit.WithinTx(ctx, func(ctx context.Context) error {
		return s.audit.Record(ctx, p, model.AuditEntry{
			Action: action, Entity: model.AuditCompany, EntityID: c.ID.String(), CompanyID: &c.ID, Changes: ch,
		})
	})
}

// describeCard is how a card appears in the audit log: never more than
// brand, last 4 digits and id.
func describeCard(pm *model.PaymentMethod) string {
	return fmt.Sprintf("%s •••• %s (%s)", pm.Brand, pm.Last4, pm.ID)
}
