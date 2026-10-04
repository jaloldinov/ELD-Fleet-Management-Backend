// Package mail sends plain-text emails (password reset codes, security
// notices).
//
// Senders, combined in main:
//   - SMTP: delivers through an SMTP server (Gmail, Mailpit in development);
//   - Async: queues messages and sends them in the background, so an HTTP
//     request never waits for the mail server;
//   - Disabled: logs that an email was not sent (no SMTP configured).
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Message is one email to one recipient.
type Message struct {
	To      string // plain address: jane@acme.com
	Subject string
	Text    string // plain-text body
}

// Sender delivers a message.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

var errHeaderInjection = errors.New("mail: header value must not contain line breaks")

// render builds the RFC 5322 message. Line breaks in To or Subject are
// rejected: they would let a value add its own headers ("header injection").
func render(from *mail.Address, m Message, now time.Time) ([]byte, error) {
	if strings.ContainsAny(m.To, "\r\n") || strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errHeaderInjection
	}
	to, err := mail.ParseAddress(m.To)
	if err != nil || to.Address != m.To {
		return nil, fmt.Errorf("mail: invalid recipient address")
	}

	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject)) // non-ASCII safe
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", messageID(from.Address))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=UTF-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(m.Text, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// messageID returns a unique Message-ID in the sender's domain (some spam
// filters penalize mail without one).
func messageID(fromAddr string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	domain := fromAddr[strings.LastIndex(fromAddr, "@")+1:]
	return "<" + hex.EncodeToString(b) + "@" + domain + ">"
}

// Disabled does not send: it logs that an email was dropped. Used when no
// SMTP server is configured. The body is never logged: it may contain a
// password reset code.
type Disabled struct {
	Log *slog.Logger
}

func (d Disabled) Send(_ context.Context, m Message) error {
	d.Log.Warn("email not sent: SMTP is not configured", "to", m.To, "subject", m.Subject)
	return nil
}
