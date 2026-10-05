package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/pkg/mail"
)

type recordingTransport struct {
	to   []Recipient
	msgs []Message
}

func (r *recordingTransport) Send(_ context.Context, to Recipient, m Message) error {
	r.to, r.msgs = append(r.to, to), append(r.msgs, m)
	return nil
}

type recordingMail struct{ sent []mail.Message }

func (r *recordingMail) Send(_ context.Context, m mail.Message) error {
	r.sent = append(r.sent, m)
	return nil
}

var resetCode = OTPCode{Purpose: PurposePasswordReset, Code: "048213", ExpiresIn: 10 * time.Minute}

func TestNotifier_RoutesByChannel(t *testing.T) {
	email, other := &recordingTransport{}, &recordingTransport{}
	n := New().Register(ChannelEmail, email).Register("telegram", other)

	require.NoError(t, n.Send(context.Background(), Recipient{Channel: "telegram", Address: "123456789"}, resetCode))

	assert.Empty(t, email.msgs)
	require.Len(t, other.msgs, 1)
	assert.Equal(t, resetCode, other.msgs[0])
}

func TestNotifier_UnknownChannel(t *testing.T) {
	err := New().Send(context.Background(), Recipient{Channel: "sms"}, resetCode)

	assert.True(t, errors.Is(err, ErrNoTransport))
}

func TestEmail_OTPCode(t *testing.T) {
	m := &recordingMail{}

	err := NewEmail(m).Send(context.Background(), Recipient{Channel: ChannelEmail, Address: "jane@acme.com", Name: "Jane"}, resetCode)

	require.NoError(t, err)
	require.Len(t, m.sent, 1)
	sent := m.sent[0]
	assert.Equal(t, "jane@acme.com", sent.To)
	assert.Equal(t, "Your ELD password reset code", sent.Subject)
	assert.NotContains(t, sent.Subject, "048213", "never in the subject (lock screens, logs)")
	assert.Contains(t, sent.Text, "Hello Jane,")
	assert.Contains(t, sent.Text, "Your password reset code is: 048213")
	assert.Contains(t, sent.Text, "expires in 10 minutes")
}

func TestEmail_GreetingWithoutName(t *testing.T) {
	m := &recordingMail{}

	require.NoError(t, NewEmail(m).Send(context.Background(), Recipient{Address: "a@b.co"}, resetCode))

	assert.Contains(t, m.sent[0].Text, "Hello,\n")
}

func TestEmail_PasswordChanged(t *testing.T) {
	m := &recordingMail{}

	require.NoError(t, NewEmail(m).Send(context.Background(), Recipient{Address: "a@b.co", Name: "Jane"}, PasswordChanged{}))

	assert.Equal(t, "Your ELD password was changed", m.sent[0].Subject)
	assert.Contains(t, m.sent[0].Text, "logged out everywhere")
}

// A purpose without wording must fail loudly, not send an empty email.
func TestEmail_UnknownPurpose(t *testing.T) {
	m := &recordingMail{}

	err := NewEmail(m).Send(context.Background(), Recipient{Address: "a@b.co"}, OTPCode{Purpose: "nope", Code: "1"})

	assert.Error(t, err)
	assert.Empty(t, m.sent)
}
