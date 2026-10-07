package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// fakeParser returns ev for the header "valid", a signature error otherwise.
type fakeParser struct{ ev *model.StripeEvent }

func (f fakeParser) Parse(_ []byte, header string) (*model.StripeEvent, error) {
	if header != "valid" {
		return nil, model.ErrWebhookSignature
	}
	return f.ev, nil
}

type memEvents struct {
	processed map[string]bool
	failed    map[string]string
	starts    int
}

func (m *memEvents) Start(_ context.Context, id, _ string) (bool, error) {
	m.starts++
	return m.processed[id], nil
}

func (m *memEvents) MarkProcessed(_ context.Context, id string) error {
	m.processed[id] = true
	return nil
}

func (m *memEvents) MarkFailed(_ context.Context, id, msg string) error {
	m.failed[id] = msg
	return nil
}

type fakeSyncer struct {
	synced []string
	err    error
}

func (f *fakeSyncer) SyncByStripeID(_ context.Context, id string) (*model.Subscription, error) {
	f.synced = append(f.synced, id)
	if f.err != nil {
		return nil, f.err
	}
	return &model.Subscription{CompanyID: uuid.New(), StripeSubscriptionID: id, Status: model.SubscriptionActive}, nil
}

// fakeInvoices: in_sub belongs to sub_1, in_oneoff to no subscription.
type fakeInvoices struct{}

func (fakeInvoices) InvoiceSubscriptionID(_ context.Context, id string) (string, error) {
	switch id {
	case "in_sub":
		return "sub_1", nil
	case "in_oneoff":
		return "", nil
	default:
		return "", model.ErrNotFound
	}
}

func newWebhookSvc(ev *model.StripeEvent) (*WebhookService, *memEvents, *fakeSyncer) {
	events := &memEvents{processed: map[string]bool{}, failed: map[string]string{}}
	syncer := &fakeSyncer{}
	return NewWebhookService(fakeParser{ev}, events, syncer, fakeInvoices{}, slog.New(slog.DiscardHandler)), events, syncer
}

func handle(svc *WebhookService) error {
	return svc.Handle(context.Background(), []byte("{}"), "valid")
}

func TestWebhook_SubscriptionEventSyncsIt(t *testing.T) {
	svc, events, syncer := newWebhookSvc(&model.StripeEvent{ID: "evt_1", Type: "customer.subscription.updated", ObjectID: "sub_9"})

	require.NoError(t, handle(svc))

	assert.Equal(t, []string{"sub_9"}, syncer.synced)
	assert.True(t, events.processed["evt_1"])
}

func TestWebhook_InvoiceEventSyncsItsSubscription(t *testing.T) {
	for _, typ := range []string{"invoice.paid", "invoice.payment_failed", "invoice.payment_action_required"} {
		t.Run(typ, func(t *testing.T) {
			svc, _, syncer := newWebhookSvc(&model.StripeEvent{ID: "evt_1", Type: typ, ObjectID: "in_sub"})
			require.NoError(t, handle(svc))
			assert.Equal(t, []string{"sub_1"}, syncer.synced)
		})
	}
}

func TestWebhook_DeliveredTwiceProcessedOnce(t *testing.T) {
	svc, events, syncer := newWebhookSvc(&model.StripeEvent{ID: "evt_1", Type: "invoice.paid", ObjectID: "in_sub"})

	require.NoError(t, handle(svc))
	require.NoError(t, handle(svc))

	assert.Equal(t, 2, events.starts)
	assert.Len(t, syncer.synced, 1, "the second delivery is acknowledged, not processed")
}

func TestWebhook_BadSignature(t *testing.T) {
	svc, events, syncer := newWebhookSvc(&model.StripeEvent{ID: "evt_1", Type: "invoice.paid"})

	err := svc.Handle(context.Background(), []byte("{}"), "forged")

	assert.ErrorIs(t, err, model.ErrWebhookSignature)
	assert.Zero(t, events.starts, "nothing is recorded for a forged request")
	assert.Empty(t, syncer.synced)
}

func TestWebhook_FailureIsRetried(t *testing.T) {
	svc, events, syncer := newWebhookSvc(&model.StripeEvent{ID: "evt_1", Type: "customer.subscription.updated", ObjectID: "sub_1"})
	syncer.err = errStripeDown

	err := handle(svc)

	require.Error(t, err, "an error makes Stripe retry")
	assert.False(t, events.processed["evt_1"])
	assert.Contains(t, events.failed["evt_1"], "stripe is down")

	// Stripe's retry, once things work again.
	syncer.err = nil
	require.NoError(t, handle(svc))
	assert.True(t, events.processed["evt_1"])
}

func TestWebhook_AcknowledgedWithoutWork(t *testing.T) {
	cases := map[string]*model.StripeEvent{
		"type we do not handle":      {ID: "evt_1", Type: "charge.refunded", ObjectID: "ch_1"},
		"invoice of no subscription": {ID: "evt_2", Type: "invoice.paid", ObjectID: "in_oneoff"},
		"invoice Stripe cannot find": {ID: "evt_3", Type: "invoice.paid", ObjectID: "in_gone"},
	}
	for name, ev := range cases {
		t.Run(name, func(t *testing.T) {
			svc, events, syncer := newWebhookSvc(ev)
			require.NoError(t, handle(svc), "answered 200, or Stripe retries it for 3 days")
			assert.Empty(t, syncer.synced)
			assert.True(t, events.processed[ev.ID])
		})
	}
}

func TestWebhook_NotOursIsAcknowledged(t *testing.T) {
	svc, events, syncer := newWebhookSvc(&model.StripeEvent{ID: "evt_1", Type: "customer.subscription.created", ObjectID: "sub_x"})
	syncer.err = ErrNotOurs

	require.NoError(t, handle(svc))
	assert.True(t, events.processed["evt_1"], "another app's subscription: acknowledged, not retried")
}
