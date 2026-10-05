package e2e

import (
	"bufio"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// email is one message the API sent.
type email struct {
	To      string
	Subject string
	Body    string
}

// inbox is a tiny SMTP server that keeps every message in memory.
//
// It speaks just enough SMTP for net/smtp's client: no STARTTLS and no AUTH
// offered, which is what pkg/mail does with Mailpit in development.
type inbox struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []email
}

func startInbox() (*inbox, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	in := &inbox{ln: ln}
	go in.serve()
	return in, nil
}

func (in *inbox) Port() string {
	_, port, _ := net.SplitHostPort(in.ln.Addr().String())
	return port
}

func (in *inbox) Close() { in.ln.Close() }

func (in *inbox) serve() {
	for {
		conn, err := in.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go in.handle(conn)
	}
}

func (in *inbox) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	tp := textproto.NewConn(conn)
	reply := func(line string) { _ = tp.PrintfLine("%s", line) }

	reply("220 e2e ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		switch cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); cmd {
		case "EHLO", "HELO":
			reply("250 e2e")
		case "MAIL", "RCPT", "RSET", "NOOP":
			reply("250 OK")
		case "DATA":
			reply("354 end with <CRLF>.<CRLF>")
			raw, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			if m, err := parseEmail(raw); err == nil {
				in.mu.Lock()
				in.msgs = append(in.msgs, m)
				in.mu.Unlock()
			}
			reply("250 OK")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func parseEmail(raw []byte) (email, error) {
	msg, err := mail.ReadMessage(bufio.NewReader(strings.NewReader(string(raw))))
	if err != nil {
		return email{}, err
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		return email{}, err
	}
	to, err := mail.ParseAddress(msg.Header.Get("To"))
	if err != nil {
		return email{}, err
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		return email{}, err
	}
	return email{To: to.Address, Subject: subject, Body: string(body)}, nil
}

// waitFor returns the first message to `to` whose subject contains
// subject. Emails are sent in the background, so it polls for a while.
func (in *inbox) waitFor(t *testing.T, to, subject string) email {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := in.find(to, subject); ok {
			return m
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.FailNow(t, "no email arrived", "to %s with subject containing %q", to, subject)
	return email{}
}

// count is how many messages went to `to` so far.
func (in *inbox) count(to string) int {
	in.mu.Lock()
	defer in.mu.Unlock()
	n := 0
	for _, m := range in.msgs {
		if strings.EqualFold(m.To, to) {
			n++
		}
	}
	return n
}

func (in *inbox) find(to, subject string) (email, bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, m := range in.msgs {
		if strings.EqualFold(m.To, to) && strings.Contains(m.Subject, subject) {
			return m, true
		}
	}
	return email{}, false
}
