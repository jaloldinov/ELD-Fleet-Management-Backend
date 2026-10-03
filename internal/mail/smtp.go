package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"time"
)

// SMTPConfig: where and as whom to send.
//
// Gmail: Host smtp.gmail.com, Port 587, Username the Gmail address,
// Password a 16-character App Password (Google Account → Security →
// 2-Step Verification → App passwords; the normal password does not work),
// From the same Gmail address (Gmail rewrites any other From).
type SMTPConfig struct {
	Host     string
	Port     int    // 587 (STARTTLS); implicit TLS on 465 is not supported
	Username string // empty = no authentication (e.g. Mailpit)
	Password string
	From     string // plain address: noreply@example.com
	FromName string // display name shown in the inbox, e.g. "ELD"; optional
}

// SMTP sends each message over a new connection: our volume (a few emails
// per hour) does not justify keeping connections open.
type SMTP struct {
	cfg  SMTPConfig
	from *mail.Address
	now  func() time.Time
}

func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	parsed, err := mail.ParseAddress(cfg.From)
	if err != nil || parsed.Address != cfg.From {
		return nil, errors.New("mail: From must be a plain email address")
	}
	from := &mail.Address{Name: cfg.FromName, Address: cfg.From}
	if cfg.Host == "" || cfg.Port <= 0 {
		return nil, errors.New("mail: SMTP host and port are required")
	}
	return &SMTP{cfg: cfg, from: from, now: time.Now}, nil
}

// Send delivers m. The whole conversation must finish before ctx's
// deadline: a hanging mail server must not hang the caller forever.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	body, err := render(s.from, m, s.now())
	if err != nil {
		return err
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)))
	if err != nil {
		return fmt.Errorf("mail: connect: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline) // applies to every read and write below
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: greeting: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	} else if s.cfg.Username != "" {
		// Never send the password unencrypted (an attacker in the middle
		// could also strip STARTTLS from the server's reply on purpose).
		return errors.New("mail: server does not offer STARTTLS; refusing to send credentials")
	}

	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: end of data: %w", err)
	}
	return c.Quit()
}
