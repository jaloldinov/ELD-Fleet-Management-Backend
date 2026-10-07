package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// WebhookParser verifies and reads a Stripe webhook (stripegw.Webhooks).
type WebhookParser interface {
	Parse(payload []byte, signatureHeader string) (*model.StripeEvent, error)
}

// StripeEventStore remembers handled events (repository.StripeEventRepository).
type StripeEventStore interface {
	Start(ctx context.Context, id, eventType string) (alreadyProcessed bool, err error)
	MarkProcessed(ctx context.Context, id string) error
	MarkFailed(ctx context.Context, id, msg string) error
}

// SubscriptionSyncer re-reads a subscription from Stripe (SubscriptionService).
type SubscriptionSyncer interface {
	SyncByStripeID(ctx context.Context, stripeID string) (*model.Subscription, error)
}

// InvoiceSyncer re-reads an invoice from Stripe (InvoiceService).
type InvoiceSyncer interface {
	SyncByStripeID(ctx context.Context, stripeID string) (*model.Invoice, error)
}

// InvoiceLookup finds the subscription of an invoice (stripegw.Subscriptions).
type InvoiceLookup interface {
	InvoiceSubscriptionID(ctx context.Context, invoiceID string) (string, error)
}

// WebhookService handles Stripe's webhooks.
//
// A webhook is a doorbell, not a letter: "something happened to sub_123".
// We check that Stripe rang (signature), make sure we answer each ring once
// (stripe_events), then read the CURRENT state from Stripe and save it.
// Reading the current state makes late, repeated and out-of-order events
// harmless: the last sync always wins with the truth.
//
// An error makes the handler answer 500, and Stripe retries the event for
// up to 3 days. Anything we do not care about is acknowledged (200), or
// Stripe would retry it for nothing.
type WebhookService struct {
	parser   WebhookParser
	events   StripeEventStore
	subs     SubscriptionSyncer
	invoices InvoiceLookup
	bills    InvoiceSyncer
	log      *slog.Logger
}

func NewWebhookService(parser WebhookParser, events StripeEventStore, subs SubscriptionSyncer, invoices InvoiceLookup,
	bills InvoiceSyncer, log *slog.Logger) *WebhookService {
	return &WebhookService{parser: parser, events: events, subs: subs, invoices: invoices, bills: bills,
		log: log.With("component", "stripe-webhook")}
}

// Handle processes one webhook delivery. model.ErrWebhookSignature means
// it was not from Stripe.
func (s *WebhookService) Handle(ctx context.Context, payload []byte, signatureHeader string) error {
	ev, err := s.parser.Parse(payload, signatureHeader)
	if err != nil {
		return err
	}
	log := s.log.With("event_id", ev.ID, "event_type", ev.Type)

	done, err := s.events.Start(ctx, ev.ID, ev.Type)
	if err != nil {
		return err
	}
	if done {
		log.Debug("stripe event already processed; delivered again")
		return nil
	}

	if err := s.dispatch(ctx, log, ev); err != nil {
		log.Error("stripe event failed; Stripe will retry it", "error", err)
		if mErr := s.events.MarkFailed(ctx, ev.ID, err.Error()); mErr != nil {
			log.Error("could not record the failure", "error", mErr)
		}
		return err
	}
	return s.events.MarkProcessed(ctx, ev.ID)
}

func (s *WebhookService) dispatch(ctx context.Context, log *slog.Logger, ev *model.StripeEvent) error {
	var subID string
	switch ev.Type {
	case "customer.subscription.created",
		"customer.subscription.updated",
		"customer.subscription.deleted",
		"customer.subscription.paused",
		"customer.subscription.resumed":
		subID = ev.ObjectID

	case "invoice.created", "invoice.finalized", "invoice.updated", "invoice.voided":
		// Our copy of the invoice follows it; no subscription change.
		return s.syncInvoice(ctx, log, ev.ObjectID)

	case "invoice.paid",
		"invoice.payment_failed",
		"invoice.payment_action_required",
		"invoice.marked_uncollectible":
		if err := s.syncInvoice(ctx, log, ev.ObjectID); err != nil {
			return err
		}
		// An invoice also changes its subscription's status (paid: active;
		// failed: past_due). Ask Stripe which subscription it belongs to.
		id, err := s.invoices.InvoiceSubscriptionID(ctx, ev.ObjectID)
		if errors.Is(err, model.ErrNotFound) || (err == nil && id == "") {
			log.Debug("invoice of no subscription; nothing to sync", "invoice", ev.ObjectID)
			return nil
		}
		if err != nil {
			return err
		}
		subID = id

	default:
		// Stripe sends many event types; we subscribe to few. Acknowledge
		// the rest, or Stripe retries them for three days.
		log.Debug("stripe event type not handled; ignored")
		return nil
	}

	sub, err := s.subs.SyncByStripeID(ctx, subID)
	if errors.Is(err, ErrNotOurs) {
		log.Info("stripe event about a subscription that is not ours; ignored", "subscription", subID)
		return nil
	}
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if ev.Type == "invoice.payment_failed" || sub.Status == model.SubscriptionPastDue || sub.Status == model.SubscriptionUnpaid {
		level = slog.LevelWarn // money problem: worth seeing in the logs
	}
	log.Log(ctx, level, "subscription synced from stripe event",
		"company_id", sub.CompanyID, "subscription", sub.StripeSubscriptionID, "status", sub.Status)
	return nil
}

// syncInvoice updates our copy of an invoice. Invoices of no company of
// ours (another app on the same Stripe account) are ignored.
func (s *WebhookService) syncInvoice(ctx context.Context, log *slog.Logger, invoiceID string) error {
	inv, err := s.bills.SyncByStripeID(ctx, invoiceID)
	if errors.Is(err, ErrNotOurs) {
		log.Debug("invoice of none of our companies; ignored", "invoice", invoiceID)
		return nil
	}
	if err != nil {
		return err
	}
	log.Info("invoice synced from stripe event", "company_id", inv.CompanyID, "invoice", inv.StripeInvoiceID, "status", inv.Status)
	return nil
}
