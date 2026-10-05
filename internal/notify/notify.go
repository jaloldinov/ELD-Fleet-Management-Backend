// Package notify tells users things: one-time codes (OTP) and security
// notices, over a channel (email today; Telegram or SMS later).
//
// Services say WHAT to tell (an OTPCode, a PasswordChanged notice) and to
// whom; a Transport per channel decides HOW it looks and travels. Adding a
// channel means writing one Transport and registering it in main: no
// service changes.
package notify

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Channel is how a message reaches the user.
type Channel string

const (
	ChannelEmail Channel = "email"
	// Future: ChannelTelegram Channel = "telegram" (Address = chat ID).
)

// Recipient is who gets a message, and where.
type Recipient struct {
	Channel Channel
	Address string // email address (later: Telegram chat ID, phone number)
	Name    string // for the greeting; may be empty
}

// Purpose says what a one-time code is for; channels word it accordingly.
type Purpose string

const (
	PurposePasswordReset Purpose = "password_reset"
	// Future: PurposeEmailVerify, PurposeLogin2FA, ...
)

// Message is something to tell the user. The set is closed (only this
// package defines messages), so every Transport knows every message.
type Message interface {
	message()
}

// OTPCode delivers a one-time code.
type OTPCode struct {
	Purpose   Purpose
	Code      string
	ExpiresIn time.Duration
}

// PasswordChanged tells the owner their password was just changed.
type PasswordChanged struct{}

func (OTPCode) message()         {}
func (PasswordChanged) message() {}

// Transport delivers messages over one channel.
type Transport interface {
	Send(ctx context.Context, to Recipient, m Message) error
}

// ErrNoTransport: nothing is registered for the recipient's channel.
var ErrNoTransport = errors.New("notify: channel is not configured")

// Notifier routes each message to the Transport of the recipient's channel.
type Notifier struct {
	transports map[Channel]Transport
}

func New() *Notifier {
	return &Notifier{transports: make(map[Channel]Transport)}
}

// Register makes ch deliver through t. Returns n, so calls can be chained.
func (n *Notifier) Register(ch Channel, t Transport) *Notifier {
	n.transports[ch] = t
	return n
}

func (n *Notifier) Send(ctx context.Context, to Recipient, m Message) error {
	t, ok := n.transports[to.Channel]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoTransport, to.Channel)
	}
	return t.Send(ctx, to, m)
}
