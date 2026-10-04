package mail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSMTP is a minimal SMTP server for one test: it records the commands
// and the message. extensions are advertised in the EHLO reply.
type fakeSMTP struct {
	addr string
	mu   sync.Mutex
	cmds []string
	data string
}

func startFakeSMTP(t *testing.T, extensions ...string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		reply := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
		reply("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			f.mu.Lock()
			f.cmds = append(f.cmds, line)
			f.mu.Unlock()
			switch verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); verb {
			case "EHLO":
				for _, ext := range extensions {
					reply("250-" + ext)
				}
				reply("250 fake")
			case "DATA":
				reply("354 go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				f.mu.Lock()
				f.data = b.String()
				f.mu.Unlock()
				reply("250 queued")
			case "QUIT":
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()
	return f
}

func (f *fakeSMTP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeSMTP) message() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func newTestSMTP(t *testing.T, addr, username string) *SMTP {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	s, err := NewSMTP(SMTPConfig{Host: host, Port: p, Username: username, Password: "pw", From: "noreply@eld.test", FromName: "ELD"})
	require.NoError(t, err)
	s.now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	return s
}

func TestSMTP_Send(t *testing.T) {
	srv := startFakeSMTP(t)
	s := newTestSMTP(t, srv.addr, "")

	err := s.Send(context.Background(), Message{
		To: "jane@acme.com", Subject: "Your code: 123456", Text: "Hello Jane,\nyour code is 123456.",
	})

	require.NoError(t, err)
	assert.Contains(t, srv.commands(), "MAIL FROM:<noreply@eld.test>")
	assert.Contains(t, srv.commands(), "RCPT TO:<jane@acme.com>")

	msg, err := mail.ReadMessage(strings.NewReader(srv.message()))
	require.NoError(t, err)
	assert.Equal(t, `"ELD" <noreply@eld.test>`, msg.Header.Get("From"))
	assert.Equal(t, "<jane@acme.com>", msg.Header.Get("To"))
	assert.Equal(t, "Your code: 123456", msg.Header.Get("Subject"))
	assert.Equal(t, "Sat, 03 Oct 2026 12:00:00 +0000", msg.Header.Get("Date"))
	assert.Regexp(t, `^<[0-9a-f]{32}@eld\.test>$`, msg.Header.Get("Message-ID"))
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	require.NoError(t, err)
	assert.Equal(t, "Hello Jane,\r\nyour code is 123456.", strings.TrimRight(string(body), "\r\n"), "SMTP adds a final line break")
}

func TestSMTP_NonASCIISubject(t *testing.T) {
	srv := startFakeSMTP(t)
	s := newTestSMTP(t, srv.addr, "")

	require.NoError(t, s.Send(context.Background(), Message{To: "a@b.co", Subject: "Parolni tiklash — kod", Text: "x"}))

	assert.Contains(t, srv.message(), "Subject: =?utf-8?q?", "encoded, not raw UTF-8 in a header")
}

// With credentials and no STARTTLS, the password must never be sent.
func TestSMTP_RefusesCredentialsWithoutTLS(t *testing.T) {
	srv := startFakeSMTP(t, "AUTH PLAIN")
	s := newTestSMTP(t, srv.addr, "me@gmail.com")

	err := s.Send(context.Background(), Message{To: "a@b.co", Subject: "s", Text: "x"})

	assert.ErrorContains(t, err, "STARTTLS")
	for _, c := range srv.commands() {
		assert.False(t, strings.HasPrefix(strings.ToUpper(c), "AUTH"), "no AUTH command sent")
	}
}

func TestSMTP_HeaderInjectionRejected(t *testing.T) {
	s := newTestSMTP(t, "127.0.0.1:1", "")

	for _, m := range []Message{
		{To: "a@b.co\r\nBcc: victim@x.com", Subject: "s"},
		{To: "a@b.co", Subject: "hi\r\nBcc: victim@x.com"},
		{To: "Jane <a@b.co>", Subject: "s"}, // only plain addresses
		{To: "not-an-address", Subject: "s"},
	} {
		assert.Error(t, s.Send(context.Background(), m), "%q", m.To+" / "+m.Subject)
	}
}

func TestSMTP_TimesOutOnASilentServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() { // accepts, then never says anything
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(5 * time.Second)
		}
	}()
	s := newTestSMTP(t, ln.Addr().String(), "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = s.Send(ctx, Message{To: "a@b.co", Subject: "s", Text: "x"})

	assert.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestNewSMTP_Validation(t *testing.T) {
	_, err := NewSMTP(SMTPConfig{Host: "h", Port: 587, From: "not an address"})
	assert.Error(t, err)
	_, err = NewSMTP(SMTPConfig{Host: "h", Port: 587, From: "ELD <a@b.co>"})
	assert.Error(t, err, "the name goes in FromName")
	_, err = NewSMTP(SMTPConfig{Port: 587, From: "a@b.co"})
	assert.Error(t, err)
}

// ---------- Async ----------

type recordingSender struct {
	mu   sync.Mutex
	sent []Message
	err  error
	wait chan struct{} // if set, Send blocks until closed
}

func (r *recordingSender) Send(_ context.Context, m Message) error {
	if r.wait != nil {
		<-r.wait
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, m)
	return r.err
}

func TestAsync_DeliversInBackgroundAndCloseWaits(t *testing.T) {
	next := &recordingSender{}
	a := NewAsync(next, slog.New(slog.DiscardHandler), 10, time.Second)

	for i := range 3 {
		require.NoError(t, a.Send(context.Background(), Message{To: strconv.Itoa(i) + "@b.co"}))
	}
	require.NoError(t, a.Close(context.Background()))

	assert.Len(t, next.sent, 3, "Close waited for the queue")
	assert.Error(t, a.Send(context.Background(), Message{}), "closed")
}

func TestAsync_SendNeverBlocks(t *testing.T) {
	next := &recordingSender{wait: make(chan struct{})} // the "mail server" hangs
	a := NewAsync(next, slog.New(slog.DiscardHandler), 1, time.Second)
	defer func() { close(next.wait); _ = a.Close(context.Background()) }()

	var errs []error
	for range 5 {
		errs = append(errs, a.Send(context.Background(), Message{To: "a@b.co"}))
	}

	assert.Contains(t, errs, ErrQueueFull, "dropped instead of blocking the request")
}

func TestAsync_LogsFailures(t *testing.T) {
	var buf bytes.Buffer
	a := NewAsync(&recordingSender{err: errors.New("smtp down")}, slog.New(slog.NewTextHandler(&buf, nil)), 10, time.Second)

	require.NoError(t, a.Send(context.Background(), Message{To: "a@b.co", Subject: "Code", Text: "123456"}))
	require.NoError(t, a.Close(context.Background()))

	assert.Contains(t, buf.String(), "email delivery failed")
	assert.NotContains(t, buf.String(), "123456", "the body (a code) is never logged")
}

func TestDisabled_LogsWithoutBody(t *testing.T) {
	var buf bytes.Buffer

	require.NoError(t, Disabled{Log: slog.New(slog.NewTextHandler(&buf, nil))}.
		Send(context.Background(), Message{To: "a@b.co", Subject: "Code", Text: "123456"}))

	assert.Contains(t, buf.String(), "SMTP is not configured")
	assert.NotContains(t, buf.String(), "123456")
}
