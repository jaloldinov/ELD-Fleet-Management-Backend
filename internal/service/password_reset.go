package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/notify"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// PasswordResetRepository stores codes and reset tokens
// (repository.PasswordResetRepository).
type PasswordResetRepository interface {
	Replace(ctx context.Context, userID uuid.UUID, codeHash []byte, ttl time.Duration) error
	GetPending(ctx context.Context, userID uuid.UUID) (*model.PasswordReset, error)
	ConsumeAttempt(ctx context.Context, id uuid.UUID, maxAttempts int) error
	MarkVerified(ctx context.Context, id uuid.UUID, tokenHash []byte, ttl time.Duration) error
	UseToken(ctx context.Context, tokenHash []byte) (uuid.UUID, error)
}

// PasswordResetUsers is what the reset needs from the users
// (repository.UserRepository).
type PasswordResetUsers interface {
	GetByLogin(ctx context.Context, login string) (*model.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error
}

// Notifier tells users things over their channel (notify.Notifier): the
// service says WHAT (a code, a notice), the channel decides HOW (email
// today; Telegram or SMS later, without changes here).
type Notifier interface {
	Send(ctx context.Context, to notify.Recipient, m notify.Message) error
}

const (
	resetCodeTTL      = 10 * time.Minute
	resetTokenTTL     = 10 * time.Minute
	resetCodeAttempts = 5 // wrong guesses per code: 5 in 1,000,000
)

var resetCodeFormat = regexp.MustCompile(`^[0-9]{6}$`)

// PasswordResetService implements "forgot password":
//
//  1. RequestReset: email a 6-digit code (10 minutes, 5 attempts).
//  2. VerifyCode: correct code → one-time reset token (10 minutes).
//  3. ResetPassword: token + new password → password changed, every
//     session ended, notice emailed.
//
// To the client, every failure of a step looks the same: no answer reveals
// whether an email address has an account.
type PasswordResetService struct {
	resets   PasswordResetRepository
	users    PasswordResetUsers
	sessions SessionRevoker
	tx       Transactor
	codes    *auth.ResetCodeHasher
	hasher   PasswordHasher
	notifier Notifier
	audit    *Auditor
	log      *slog.Logger
}

func NewPasswordResetService(
	resets PasswordResetRepository, users PasswordResetUsers, sessions SessionRevoker, tx Transactor,
	codes *auth.ResetCodeHasher, hasher PasswordHasher, notifier Notifier, audit *Auditor, log *slog.Logger,
) *PasswordResetService {
	return &PasswordResetService{
		resets: resets, users: users, sessions: sessions, tx: tx,
		codes: codes, hasher: hasher, notifier: notifier, audit: audit, log: log,
	}
}

// RequestReset emails a code if email belongs to an active user. It returns
// nil whether or not it did (only a malformed address is an error), so the
// answer never tells who has an account.
func (s *PasswordResetService) RequestReset(ctx context.Context, email string) error {
	email = strings.TrimSpace(email)
	if !validation.Email(email) {
		return &validation.ValidationError{Fields: map[string]string{"email": "must be a valid email address"}}
	}

	u, err := s.activeUserByEmail(ctx, email)
	if errors.Is(err, model.ErrNotFound) {
		return nil // nothing to send, and nothing to tell
	}
	if err != nil {
		return err
	}

	code, err := auth.NewResetCode()
	if err != nil {
		return err
	}
	if err := s.resets.Replace(ctx, u.ID, s.codes.Hash(u.ID, code), resetCodeTTL); err != nil {
		return err
	}

	s.send(ctx, recipientOf(u), notify.OTPCode{
		Purpose: notify.PurposePasswordReset, Code: code, ExpiresIn: resetCodeTTL,
	})
	return nil
}

// VerifyCode checks the emailed code and returns a one-time reset token.
// Every failure is model.ErrResetCodeInvalid.
func (s *PasswordResetService) VerifyCode(ctx context.Context, email, code string) (token string, expiresIn time.Duration, err error) {
	email, code = strings.TrimSpace(email), strings.TrimSpace(code)
	if !resetCodeFormat.MatchString(code) {
		return "", 0, model.ErrResetCodeInvalid
	}

	u, err := s.activeUserByEmail(ctx, email)
	if err != nil {
		return "", 0, invalidIfNotFound(err, model.ErrResetCodeInvalid)
	}
	pr, err := s.resets.GetPending(ctx, u.ID)
	if err != nil {
		return "", 0, invalidIfNotFound(err, model.ErrResetCodeInvalid)
	}
	// Count the attempt BEFORE comparing: parallel guesses cannot get more
	// than resetCodeAttempts tries.
	if err := s.resets.ConsumeAttempt(ctx, pr.ID, resetCodeAttempts); err != nil {
		return "", 0, invalidIfNotFound(err, model.ErrResetCodeInvalid)
	}
	if !s.codes.Equal(pr.CodeHash, s.codes.Hash(u.ID, code)) {
		return "", 0, model.ErrResetCodeInvalid
	}

	plain, hash, err := auth.NewResetToken()
	if err != nil {
		return "", 0, err
	}
	if err := s.resets.MarkVerified(ctx, pr.ID, hash, resetTokenTTL); err != nil {
		return "", 0, invalidIfNotFound(err, model.ErrResetCodeInvalid)
	}
	return plain, resetTokenTTL, nil
}

// ResetPassword sets a new password with a reset token from VerifyCode, and
// ends all sessions: whoever knew the old password is logged out.
func (s *PasswordResetService) ResetPassword(ctx context.Context, token, newPassword string) error {
	// Validate first: a weak password must not use up the token.
	var v validation.Validator
	checkPassword(&v, "new_password", newPassword)
	if err := v.Err(); err != nil {
		return err
	}
	// Hash outside the transaction: bcrypt is slow on purpose, and a
	// transaction should not stay open meanwhile.
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return err
	}

	var u *model.User
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		id, err := s.resets.UseToken(ctx, auth.HashResetToken(strings.TrimSpace(token)))
		if err != nil {
			return invalidIfNotFound(err, model.ErrResetTokenInvalid)
		}
		// ErrNotFound: the user was disabled or deleted after requesting.
		if err := s.users.UpdatePassword(ctx, id, hash); err != nil {
			return invalidIfNotFound(err, model.ErrResetTokenInvalid)
		}
		if err := s.sessions.RevokeAllForUser(ctx, id); err != nil {
			return err
		}
		if u, err = s.users.GetByID(ctx, id); err != nil {
			return err
		}
		// The user did it themselves (proven by the emailed code). The
		// action alone: never the password or its hash.
		return s.audit.Record(ctx, auth.Principal{UserID: id, CompanyID: u.CompanyID}, model.AuditEntry{
			Action: "user.password_reset", Entity: model.AuditUser, EntityID: id.String(), CompanyID: u.CompanyID,
		})
	})
	if err != nil {
		return err
	}

	// Tell the owner: if they did not do this, they know at once.
	if u.Email != nil {
		s.send(ctx, recipientOf(u), notify.PasswordChanged{})
	}
	return nil
}

// activeUserByEmail: model.ErrNotFound unless email is an active user's
// email address (not just a username: "forgot password" needs a mailbox).
func (s *PasswordResetService) activeUserByEmail(ctx context.Context, email string) (*model.User, error) {
	u, err := s.users.GetByLogin(ctx, email)
	if err != nil {
		return nil, err
	}
	if !u.IsActive || u.Email == nil || !strings.EqualFold(*u.Email, email) {
		return nil, model.ErrNotFound
	}
	return u, nil
}

// recipientOf: where to reach u. Email for now (callers check u.Email);
// this is the one place to prefer another channel (e.g. a linked Telegram
// account) once users have one.
func recipientOf(u *model.User) notify.Recipient {
	return notify.Recipient{Channel: notify.ChannelEmail, Address: *u.Email, Name: u.FirstName}
}

// send hands m to the notifier. A failure (queue full, channel down) is
// logged, not returned: an error would only happen for existing accounts,
// so returning it would tell the client the account exists.
func (s *PasswordResetService) send(ctx context.Context, to notify.Recipient, m notify.Message) {
	if err := s.notifier.Send(ctx, to, m); err != nil {
		s.log.Error("could not send notification", "channel", to.Channel, "message", fmt.Sprintf("%T", m), "error", err)
	}
}

func invalidIfNotFound(err, invalid error) error {
	if errors.Is(err, model.ErrNotFound) {
		return invalid
	}
	return err
}
