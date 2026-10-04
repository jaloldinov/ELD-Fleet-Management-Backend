package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/mail"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// ---------- fakes ----------

type memReset struct {
	model.PasswordReset
	verified  bool
	used      bool
	tokenHash []byte
}

type memResets struct{ rows map[uuid.UUID]*memReset } // by reset ID

func (m *memResets) Replace(_ context.Context, userID uuid.UUID, codeHash []byte, _ time.Duration) error {
	for id, r := range m.rows {
		if r.UserID == userID && !r.used {
			delete(m.rows, id)
		}
	}
	id := uuid.New()
	m.rows[id] = &memReset{PasswordReset: model.PasswordReset{ID: id, UserID: userID, CodeHash: codeHash}}
	return nil
}

func (m *memResets) GetPending(_ context.Context, userID uuid.UUID) (*model.PasswordReset, error) {
	for _, r := range m.rows {
		if r.UserID == userID && !r.verified && !r.used {
			pr := r.PasswordReset
			return &pr, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memResets) ConsumeAttempt(_ context.Context, id uuid.UUID, maxAttempts int) error {
	r, ok := m.rows[id]
	if !ok || r.Attempts >= maxAttempts || r.verified || r.used {
		return model.ErrNotFound
	}
	r.Attempts++
	return nil
}

func (m *memResets) MarkVerified(_ context.Context, id uuid.UUID, tokenHash []byte, _ time.Duration) error {
	r, ok := m.rows[id]
	if !ok || r.verified || r.used {
		return model.ErrNotFound
	}
	r.verified, r.tokenHash = true, tokenHash
	return nil
}

func (m *memResets) UseToken(_ context.Context, tokenHash []byte) (uuid.UUID, error) {
	for _, r := range m.rows {
		if r.verified && !r.used && bytes.Equal(r.tokenHash, tokenHash) {
			r.used = true
			return r.UserID, nil
		}
	}
	return uuid.Nil, model.ErrNotFound
}

type fakeResetUsers struct {
	byID        map[uuid.UUID]*model.User
	updated     map[uuid.UUID]string
	updateInTx  bool
	updateFails error
}

func (f *fakeResetUsers) GetByLogin(_ context.Context, login string) (*model.User, error) {
	for _, u := range f.byID {
		if (u.Email != nil && *u.Email == login) || (u.Username != nil && *u.Username == login) {
			c := *u
			return &c, nil
		}
	}
	return nil, model.ErrNotFound
}

func (f *fakeResetUsers) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *u
	return &c, nil
}

func (f *fakeResetUsers) UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error {
	if f.updateFails != nil {
		return f.updateFails
	}
	f.updateInTx = inTx(ctx)
	f.updated[id] = hash
	return nil
}

type recordingMailer struct {
	sent []mail.Message
	err  error
}

func (r *recordingMailer) Send(_ context.Context, m mail.Message) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, m)
	return nil
}

type resetFixture struct {
	svc     *PasswordResetService
	resets  *memResets
	users   *fakeResetUsers
	revoker *fakeRevoker
	tx      *fakeTx
	mailer  *recordingMailer
	audit   *memAuditStore
	log     *bytes.Buffer
	jane    *model.User
}

func newResetFixture(t *testing.T) *resetFixture {
	t.Helper()
	email := "jane@acme.com"
	jane := &model.User{ID: uuid.New(), Email: &email, FirstName: "Jane", IsActive: true}
	f := &resetFixture{
		resets:  &memResets{rows: map[uuid.UUID]*memReset{}},
		users:   &fakeResetUsers{byID: map[uuid.UUID]*model.User{jane.ID: jane}, updated: map[uuid.UUID]string{}},
		revoker: &fakeRevoker{},
		tx:      &fakeTx{},
		mailer:  &recordingMailer{},
		audit:   &memAuditStore{},
		log:     &bytes.Buffer{},
		jane:    jane,
	}
	f.svc = NewPasswordResetService(f.resets, f.users, f.revoker, f.tx,
		auth.NewResetCodeHasher("test-secret-test-secret-test-1234"), fakeHasher{}, f.mailer, NewAuditor(f.tx, f.audit),
		slog.New(slog.NewTextHandler(f.log, nil)))
	return f
}

var codeInMail = regexp.MustCompile(`code is: ([0-9]{6})`)

// requestCode runs step 1 and returns the code from the email.
func (f *resetFixture) requestCode(t *testing.T) string {
	t.Helper()
	require.NoError(t, f.svc.RequestReset(context.Background(), "jane@acme.com"))
	require.NotEmpty(t, f.mailer.sent)
	m := codeInMail.FindStringSubmatch(f.mailer.sent[len(f.mailer.sent)-1].Text)
	require.Len(t, m, 2, "the email contains the code")
	return m[1]
}

func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

// ---------- step 1: request ----------

func TestRequestReset_EmailsACode(t *testing.T) {
	f := newResetFixture(t)

	code := f.requestCode(t)

	m := f.mailer.sent[0]
	assert.Equal(t, "jane@acme.com", m.To)
	assert.NotContains(t, m.Subject, code, "never in the subject (lock screens, logs)")
	assert.Contains(t, m.Text, "Hello Jane")
	assert.Contains(t, m.Text, "10 minutes")
	pr, err := f.resets.GetPending(context.Background(), f.jane.ID)
	require.NoError(t, err)
	assert.NotContains(t, string(pr.CodeHash), code, "only the hash is stored")
}

// Same answer (nil) and no email for every kind of "no": the response must
// not reveal who has an account.
func TestRequestReset_SilentWhenNoAccount(t *testing.T) {
	username := "driver1"
	disabledEmail := "off@acme.com"
	tests := map[string]*model.User{
		"unknown email": nil,
		"disabled user": {ID: uuid.New(), Email: &disabledEmail, IsActive: false},
	}
	for name, u := range tests {
		t.Run(name, func(t *testing.T) {
			f := newResetFixture(t)
			email := "nobody@acme.com"
			if u != nil {
				f.users.byID[u.ID] = u
				email = *u.Email
			}

			assert.NoError(t, f.svc.RequestReset(context.Background(), email))
			assert.Empty(t, f.mailer.sent)
		})
	}

	t.Run("username-only user", func(t *testing.T) {
		f := newResetFixture(t)
		f.users.byID[uuid.New()] = &model.User{Username: &username, IsActive: true}

		assert.NoError(t, f.svc.RequestReset(context.Background(), "driver1@acme.com"))
		assert.Empty(t, f.mailer.sent)
	})
}

func TestRequestReset_InvalidEmail(t *testing.T) {
	f := newResetFixture(t)

	err := f.svc.RequestReset(context.Background(), "not-an-email")

	var vErr *validation.ValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Contains(t, vErr.Fields, "email")
}

func TestRequestReset_MailerFailureIsLoggedNotReturned(t *testing.T) {
	f := newResetFixture(t)
	f.mailer.err = mail.ErrQueueFull

	err := f.svc.RequestReset(context.Background(), "jane@acme.com")

	assert.NoError(t, err, "an error only for existing accounts would reveal them")
	assert.Contains(t, f.log.String(), "could not queue email")
}

func TestRequestReset_NewCodeReplacesOld(t *testing.T) {
	f := newResetFixture(t)
	old := f.requestCode(t)
	newCode := f.requestCode(t)
	if old == newCode {
		t.Skip("the two random codes happen to be equal")
	}

	_, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", old)

	assert.ErrorIs(t, err, model.ErrResetCodeInvalid)
}

// ---------- step 2: verify ----------

func TestVerifyCode_ReturnsResetToken(t *testing.T) {
	f := newResetFixture(t)
	code := f.requestCode(t)

	token, expiresIn, err := f.svc.VerifyCode(context.Background(), " jane@acme.com ", code)

	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.Equal(t, 10*time.Minute, expiresIn)

	_, _, err = f.svc.VerifyCode(context.Background(), "jane@acme.com", code)
	assert.ErrorIs(t, err, model.ErrResetCodeInvalid, "a code works once")
}

func TestVerifyCode_FiveWrongGuessesKillTheCode(t *testing.T) {
	f := newResetFixture(t)
	code := f.requestCode(t)

	for range 5 {
		_, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", wrongCode(code))
		require.ErrorIs(t, err, model.ErrResetCodeInvalid)
	}
	_, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", code)

	assert.ErrorIs(t, err, model.ErrResetCodeInvalid, "even the right code fails after 5 wrong guesses")
}

func TestVerifyCode_FailuresLookTheSame(t *testing.T) {
	f := newResetFixture(t)
	code := f.requestCode(t)

	for name, tc := range map[string][2]string{
		"unknown email":  {"nobody@acme.com", code},
		"malformed code": {"jane@acme.com", "12ab"},
		"wrong code":     {"jane@acme.com", wrongCode(code)},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := f.svc.VerifyCode(context.Background(), tc[0], tc[1])
			assert.ErrorIs(t, err, model.ErrResetCodeInvalid)
		})
	}

	t.Run("no code requested", func(t *testing.T) {
		fresh := newResetFixture(t)
		_, _, err := fresh.svc.VerifyCode(context.Background(), "jane@acme.com", "123456")
		assert.ErrorIs(t, err, model.ErrResetCodeInvalid)
	})
}

func TestVerifyCode_MalformedCodeDoesNotUseAnAttempt(t *testing.T) {
	f := newResetFixture(t)
	f.requestCode(t)

	_, _, _ = f.svc.VerifyCode(context.Background(), "jane@acme.com", "abc")

	pr, err := f.resets.GetPending(context.Background(), f.jane.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, pr.Attempts)
}

// ---------- step 3: reset ----------

func TestResetPassword_FullFlow(t *testing.T) {
	f := newResetFixture(t)
	code := f.requestCode(t)
	token, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", code)
	require.NoError(t, err)

	require.NoError(t, f.svc.ResetPassword(context.Background(), token, "new-strong-password"))

	assert.Equal(t, "hashed:new-strong-password", f.users.updated[f.jane.ID])
	assert.True(t, f.users.updateInTx, "password change and logout are one transaction")
	assert.Equal(t, []uuid.UUID{f.jane.ID}, f.revoker.revoked, "logged out everywhere")
	last := f.mailer.sent[len(f.mailer.sent)-1]
	assert.Equal(t, "Your ELD password was changed", last.Subject, "the owner is told")

	err = f.svc.ResetPassword(context.Background(), token, "another-password")
	assert.ErrorIs(t, err, model.ErrResetTokenInvalid, "a token works once")
}

func TestResetPassword_WeakPasswordKeepsTheToken(t *testing.T) {
	f := newResetFixture(t)
	token, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", f.requestCode(t))
	require.NoError(t, err)

	err = f.svc.ResetPassword(context.Background(), token, "short")

	var vErr *validation.ValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Contains(t, vErr.Fields, "new_password")
	assert.NoError(t, f.svc.ResetPassword(context.Background(), token, "long-enough-now"),
		"the token was not used up by the rejected attempt")
}

func TestResetPassword_InvalidToken(t *testing.T) {
	f := newResetFixture(t)

	err := f.svc.ResetPassword(context.Background(), "made-up-token", "new-strong-password")

	assert.ErrorIs(t, err, model.ErrResetTokenInvalid)
	assert.Empty(t, f.revoker.revoked)
}

func TestResetPassword_UserDisabledMeanwhile(t *testing.T) {
	f := newResetFixture(t)
	token, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", f.requestCode(t))
	require.NoError(t, err)
	f.users.updateFails = model.ErrNotFound

	err = f.svc.ResetPassword(context.Background(), token, "new-strong-password")

	assert.ErrorIs(t, err, model.ErrResetTokenInvalid)
}

func TestResetPassword_UnexpectedErrorIsReturned(t *testing.T) {
	f := newResetFixture(t)
	token, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", f.requestCode(t))
	require.NoError(t, err)
	f.users.updateFails = errors.New("db down")

	err = f.svc.ResetPassword(context.Background(), token, "new-strong-password")

	assert.EqualError(t, err, "db down", "a real failure is a 500, not 'invalid token'")
}

func TestResetPassword_Audited(t *testing.T) {
	f := newResetFixture(t)
	token, _, err := f.svc.VerifyCode(context.Background(), "jane@acme.com", f.requestCode(t))
	require.NoError(t, err)

	require.NoError(t, f.svc.ResetPassword(context.Background(), token, "new-strong-password"))

	e := f.audit.last(t)
	assert.Equal(t, "user.password_reset", e.Action)
	require.NotNil(t, e.ActorID)
	assert.Equal(t, f.jane.ID, *e.ActorID, "the user did it themselves")
	assert.Nil(t, e.Changes, "never the password")
	f.audit.requireAllInTx(t)
}
