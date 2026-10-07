package stripegw

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v87"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const testWebhookSecret = "whsec_test_secret"

const eventJSON = `{"id":"evt_1","object":"event","type":"invoice.payment_failed","livemode":false,
	"api_version":"2020-08-27","data":{"object":{"id":"in_1","object":"invoice","amount_due":15000}}}`

// sign makes the Stripe-Signature header Stripe would send.
func sign(payload, secret string, at time.Time) string {
	return stripe.GenerateTestSignedPayload(&stripe.UnsignedPayload{
		Payload: []byte(payload), Secret: secret, Timestamp: at,
	}).Header
}

func TestWebhooks_Parse(t *testing.T) {
	ev, err := NewWebhooks(testWebhookSecret).Parse([]byte(eventJSON), sign(eventJSON, testWebhookSecret, time.Now()))

	require.NoError(t, err)
	assert.Equal(t, model.StripeEvent{ID: "evt_1", Type: "invoice.payment_failed", ObjectID: "in_1", ObjectType: "invoice"}, *ev,
		"an old api_version is fine: we only read ids")
}

func TestWebhooks_RefusesWhatStripeDidNotSign(t *testing.T) {
	w := NewWebhooks(testWebhookSecret)
	cases := map[string]struct {
		payload, header string
	}{
		"no signature":     {eventJSON, ""},
		"wrong secret":     {eventJSON, sign(eventJSON, "whsec_attacker", time.Now())},
		"changed payload":  {`{"id":"evt_1","object":"event","type":"invoice.paid"}`, sign(eventJSON, testWebhookSecret, time.Now())},
		"replayed (10min)": {eventJSON, sign(eventJSON, testWebhookSecret, time.Now().Add(-10*time.Minute))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := w.Parse([]byte(tc.payload), tc.header)
			assert.ErrorIs(t, err, model.ErrWebhookSignature)
		})
	}
}
