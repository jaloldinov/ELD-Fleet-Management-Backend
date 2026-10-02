package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeAuthUserRepo struct {
	users          map[string]*model.User // by login
	getErr         error
	lastLoginCalls []uuid.UUID
}

func (f *fakeAuthUserRepo) GetByLogin(_ context.Context, login string) (*model.User, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	u, ok := f.users[login]
	if !ok {
		return nil, model.ErrNotFound
	}
	return u, nil
}

func (f *fakeAuthUserRepo) UpdateLastLogin(_ context.Context, id uuid.UUID) error {
	f.lastLoginCalls = append(f.lastLoginCalls, id)
	return nil
}

// fakeChecker records which hashes were compared, so we can prove the dummy
// hash is used for unknown users (the timing defence).
type fakeChecker struct {
	compared []string
}

func (f *fakeChecker) Hash(pw string) (string, error) { return "hashed:" + pw, nil }

func (f *fakeChecker) Compare(hash, pw string) bool {
	f.compared = append(f.compared, hash)
	return hash == "hashed:"+pw
}

type fakeIssuer struct {
	gotUserID    uuid.UUID
	gotCompanyID *uuid.UUID
	err          error
}

func (f *fakeIssuer) Issue(userID uuid.UUID, companyID *uuid.UUID) (string, time.Time, error) {
	f.gotUserID, f.gotCompanyID = userID, companyID
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	return "signed-token", time.Date(2026, 10, 2, 12, 15, 0, 0, time.UTC), nil
}

type authFixture struct {
	svc     *AuthService
	repo    *fakeAuthUserRepo
	checker *fakeChecker
	issuer  *fakeIssuer
	jane    *model.User
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	companyID := uuid.New()
	jane := &model.User{
		ID: uuid.New(), CompanyID: &companyID,
		PasswordHash: "hashed:correct horse", IsActive: true,
	}
	f := &authFixture{
		repo:    &fakeAuthUserRepo{users: map[string]*model.User{"jane@acme.com": jane}},
		checker: &fakeChecker{},
		issuer:  &fakeIssuer{},
		jane:    jane,
	}
	svc, err := NewAuthService(f.repo, f.checker, f.issuer)
	require.NoError(t, err)
	f.svc = svc
	return f
}

func TestAuthService_Login_Success(t *testing.T) {
	f := newAuthFixture(t)

	res, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse")

	require.NoError(t, err)
	assert.Equal(t, "signed-token", res.AccessToken)
	assert.Equal(t, f.jane.ID, f.issuer.gotUserID, "token is issued for this user")
	assert.Equal(t, f.jane.CompanyID, f.issuer.gotCompanyID, "token carries the user's company")
	assert.Equal(t, []uuid.UUID{f.jane.ID}, f.repo.lastLoginCalls)
}

func TestAuthService_Login_WrongPassword(t *testing.T) {
	f := newAuthFixture(t)

	_, err := f.svc.Login(context.Background(), "jane@acme.com", "wrong")

	assert.ErrorIs(t, err, model.ErrInvalidCredentials)
	assert.Empty(t, f.repo.lastLoginCalls)
}

func TestAuthService_Login_UnknownUser_SameErrorAndStillHashes(t *testing.T) {
	f := newAuthFixture(t)

	_, err := f.svc.Login(context.Background(), "nobody@acme.com", "whatever")

	assert.ErrorIs(t, err, model.ErrInvalidCredentials, "same error as a wrong password")
	require.Len(t, f.checker.compared, 1, "bcrypt must still run, so timing is the same")
	assert.Equal(t, f.svc.dummyHash, f.checker.compared[0])
}

func TestAuthService_Login_Inactive(t *testing.T) {
	f := newAuthFixture(t)
	f.jane.IsActive = false

	t.Run("correct password reveals disabled account", func(t *testing.T) {
		_, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse")
		assert.ErrorIs(t, err, model.ErrUserInactive)
	})

	t.Run("wrong password does not reveal it", func(t *testing.T) {
		_, err := f.svc.Login(context.Background(), "jane@acme.com", "wrong")
		assert.ErrorIs(t, err, model.ErrInvalidCredentials)
	})
}

func TestAuthService_Login_Errors(t *testing.T) {
	t.Run("repository failure is returned, not hidden as bad credentials", func(t *testing.T) {
		f := newAuthFixture(t)
		f.repo.getErr = errors.New("db down")

		_, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse")

		assert.EqualError(t, err, "db down")
	})

	t.Run("token signing failure", func(t *testing.T) {
		f := newAuthFixture(t)
		f.issuer.err = errors.New("boom")

		_, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse")

		assert.ErrorContains(t, err, "issue token")
	})
}
