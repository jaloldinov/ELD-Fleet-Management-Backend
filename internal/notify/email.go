package notify

import (
	"context"
	"fmt"

	"github.com/jaloldinov/ELD-learning/pkg/mail"
)

// Email delivers messages as plain-text emails through a mail.Sender
// (mail.Async in production, so a request never waits for the server).
type Email struct {
	mail mail.Sender
}

func NewEmail(s mail.Sender) *Email {
	return &Email{mail: s}
}

func (e *Email) Send(ctx context.Context, to Recipient, m Message) error {
	subject, text, err := renderEmail(to, m)
	if err != nil {
		return err
	}
	return e.mail.Send(ctx, mail.Message{To: to.Address, Subject: subject, Text: text})
}

// otpEmail is the wording of a code email, per purpose.
type otpEmail struct {
	subject string
	what    string // "Your password reset code"
	ignore  string // what happens if the user did not ask
}

var otpEmails = map[Purpose]otpEmail{
	PurposePasswordReset: {
		subject: "Your ELD password reset code",
		what:    "Your password reset code",
		ignore:  "If you did not ask to reset your password, ignore this email: your password stays unchanged.",
	},
}

// renderEmail turns a message into a subject and body. The code is never
// in the subject: subjects show up on lock screens and in our logs
// (mail.Async logs the subject of failed deliveries).
func renderEmail(to Recipient, m Message) (subject, text string, err error) {
	greeting := "Hello,"
	if to.Name != "" {
		greeting = "Hello " + to.Name + ","
	}

	switch m := m.(type) {
	case OTPCode:
		w, ok := otpEmails[m.Purpose]
		if !ok {
			return "", "", fmt.Errorf("notify: no email wording for purpose %q", m.Purpose)
		}
		return w.subject, fmt.Sprintf("%s\n\n%s is: %s\n\nIt expires in %d minutes. %s\n\n— ELD",
			greeting, w.what, m.Code, int(m.ExpiresIn.Minutes()), w.ignore), nil
	case PasswordChanged:
		return "Your ELD password was changed", greeting + "\n\n" +
			"The password of your ELD account was just changed, and you were logged out everywhere.\n\n" +
			"If this was not you, contact your administrator immediately.\n\n— ELD", nil
	default:
		return "", "", fmt.Errorf("notify: email cannot render %T", m)
	}
}
