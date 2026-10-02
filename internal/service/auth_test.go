package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeAuthUserRepo struct {
	byLogin map[string]*model.User
	byID    map[uuid.UUID]*model.User
	getErr  error
}

func (f *fakeAuthUserRepo) GetByLogin(_ context.Context, login string) (*model.User, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if u, ok := f.byLogin[login]; ok {
		return u, nil
	}
	return nil, model.ErrNotFound
}

func (f *fakeAuthUserRepo) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, model.ErrNotFound
}

// fakeSessions records what the service asks the repository to do.
type fakeSessions struct {
	created      []model.RefreshToken
	rotateHash   []byte
	rotateNext   model.RefreshToken
	rotateUserID uuid.UUID
	rotateErr    error
	revoked      [][]byte
	revokedAll   []uuid.UUID
}

func (f *fakeSessions) CreateForLogin(_ context.Context, t model.RefreshToken) error {
	f.created = append(f.created, t)
	return nil
}

func (f *fakeSessions) Rotate(_ context.Context, hash []byte, next model.RefreshToken) (uuid.UUID, error) {
	f.rotateHash, f.rotateNext = hash, next
	return f.rotateUserID, f.rotateErr
}

func (f *fakeSessions) Revoke(_ context.Context, hash []byte) error {
	f.revoked = append(f.revoked, hash)
	return nil
}

func (f *fakeSessions) RevokeAllForUser(_ context.Context, id uuid.UUID) error {
	f.revokedAll = append(f.revokedAll, id)
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
	return "signed-access-token", time.Now().Add(15 * time.Minute), nil
}

var testClient = model.ClientInfo{UserAgent: "ELD-App/1.0", IP: "203.0.113.7"}

type authFixture struct {
	svc      *AuthService
	users    *fakeAuthUserRepo
	sessions *fakeSessions
	checker  *fakeChecker
	issuer   *fakeIssuer
	jane     *model.User
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	companyID := uuid.New()
	jane := &model.User{
		ID: uuid.New(), CompanyID: &companyID,
		PasswordHash: "hashed:correct horse", IsActive: true,
	}
	f := &authFixture{
		users: &fakeAuthUserRepo{
			byLogin: map[string]*model.User{"jane@acme.com": jane},
			byID:    map[uuid.UUID]*model.User{jane.ID: jane},
		},
		sessions: &fakeSessions{},
		checker:  &fakeChecker{},
		issuer:   &fakeIssuer{},
		jane:     jane,
	}
	svc, err := NewAuthService(f.users, f.sessions, f.checker, f.issuer, 30*24*time.Hour)
	require.NoError(t, err)
	f.svc = svc
	return f
}

// ---------- Login ----------

func TestAuthService_Login_Success(t *testing.T) {
	f := newAuthFixture(t)

	pair, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse", testClient)

	require.NoError(t, err)
	assert.Equal(t, "signed-access-token", pair.AccessToken)
	assert.Equal(t, f.jane.ID, f.issuer.gotUserID, "token is issued for this user")
	assert.Equal(t, f.jane.CompanyID, f.issuer.gotCompanyID, "token carries the user's company")

	require.Len(t, f.sessions.created, 1, "a session is stored")
	stored := f.sessions.created[0]
	assert.Equal(t, f.jane.ID, stored.UserID)
	assert.Equal(t, auth.HashRefreshToken(pair.RefreshToken), stored.TokenHash,
		"only the HASH of the returned refresh token is stored")
	assert.NotContains(t, string(stored.TokenHash), pair.RefreshToken)
	assert.Equal(t, testClient.UserAgent, stored.UserAgent)
	assert.Equal(t, testClient.IP, stored.IP)
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), stored.ExpiresAt, time.Minute)
}

func TestAuthService_Login_WrongPassword(t *testing.T) {
	f := newAuthFixture(t)

	_, err := f.svc.Login(context.Background(), "jane@acme.com", "wrong", testClient)

	assert.ErrorIs(t, err, model.ErrInvalidCredentials)
	assert.Empty(t, f.sessions.created, "no session for a failed login")
}

func TestAuthService_Login_UnknownUser_SameErrorAndStillHashes(t *testing.T) {
	f := newAuthFixture(t)

	_, err := f.svc.Login(context.Background(), "nobody@acme.com", "whatever", testClient)

	assert.ErrorIs(t, err, model.ErrInvalidCredentials, "same error as a wrong password")
	require.Len(t, f.checker.compared, 1, "bcrypt must still run, so timing is the same")
	assert.Equal(t, f.svc.dummyHash, f.checker.compared[0])
}

func TestAuthService_Login_Inactive(t *testing.T) {
	f := newAuthFixture(t)
	f.jane.IsActive = false

	t.Run("correct password reveals disabled account", func(t *testing.T) {
		_, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse", testClient)
		assert.ErrorIs(t, err, model.ErrUserInactive)
	})

	t.Run("wrong password does not reveal it", func(t *testing.T) {
		_, err := f.svc.Login(context.Background(), "jane@acme.com", "wrong", testClient)
		assert.ErrorIs(t, err, model.ErrInvalidCredentials)
	})

	assert.Empty(t, f.sessions.created)
}

func TestAuthService_Login_Errors(t *testing.T) {
	t.Run("repository failure is returned, not hidden as bad credentials", func(t *testing.T) {
		f := newAuthFixture(t)
		f.users.getErr = errors.New("db down")

		_, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse", testClient)

		assert.EqualError(t, err, "db down")
	})

	t.Run("token signing failure", func(t *testing.T) {
		f := newAuthFixture(t)
		f.issuer.err = errors.New("boom")

		_, err := f.svc.Login(context.Background(), "jane@acme.com", "correct horse", testClient)

		assert.ErrorContains(t, err, "issue token")
	})
}

// ---------- Refresh ----------

func TestAuthService_Refresh_Success(t *testing.T) {
	f := newAuthFixture(t)
	f.sessions.rotateUserID = f.jane.ID

	pair, err := f.svc.Refresh(context.Background(), "old-refresh-token", testClient)

	require.NoError(t, err)
	assert.Equal(t, auth.HashRefreshToken("old-refresh-token"), f.sessions.rotateHash,
		"the presented token is looked up by its hash")
	assert.Equal(t, auth.HashRefreshToken(pair.RefreshToken), f.sessions.rotateNext.TokenHash,
		"the new token's hash is stored")
	assert.NotEqual(t, "old-refresh-token", pair.RefreshToken, "a NEW refresh token is returned")
	assert.Equal(t, "signed-access-token", pair.AccessToken)
	assert.Equal(t, f.jane.ID, f.issuer.gotUserID)
}

func TestAuthService_Refresh_RotateErrorsPassThrough(t *testing.T) {
	for _, rotateErr := range []error{model.ErrRefreshTokenInvalid, model.ErrRefreshTokenReused} {
		t.Run(rotateErr.Error(), func(t *testing.T) {
			f := newAuthFixture(t)
			f.sessions.rotateErr = rotateErr

			_, err := f.svc.Refresh(context.Background(), "token", testClient)

			assert.ErrorIs(t, err, rotateErr)
		})
	}
}

func TestAuthService_Refresh_DeactivatedUserLosesAllSessions(t *testing.T) {
	f := newAuthFixture(t)
	f.sessions.rotateUserID = f.jane.ID
	f.jane.IsActive = false

	_, err := f.svc.Refresh(context.Background(), "token", testClient)

	assert.ErrorIs(t, err, model.ErrUserInactive)
	assert.Equal(t, []uuid.UUID{f.jane.ID}, f.sessions.revokedAll)
}

func TestAuthService_Refresh_DeletedUser(t *testing.T) {
	f := newAuthFixture(t)
	f.sessions.rotateUserID = uuid.New() // not in the user repository

	_, err := f.svc.Refresh(context.Background(), "token", testClient)

	assert.ErrorIs(t, err, model.ErrRefreshTokenInvalid)
	assert.Len(t, f.sessions.revokedAll, 1)
}

// ---------- Logout ----------

func TestAuthService_Logout(t *testing.T) {
	f := newAuthFixture(t)

	require.NoError(t, f.svc.Logout(context.Background(), "my-refresh-token"))

	assert.Equal(t, [][]byte{auth.HashRefreshToken("my-refresh-token")}, f.sessions.revoked)
}
