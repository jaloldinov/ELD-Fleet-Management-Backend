package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeUserRepo struct {
	t         *testing.T
	createFn  func(u model.User, roles []string) (*model.User, error)
	getByIDFn func(id uuid.UUID) (*model.User, error)
	permsFn   func(id uuid.UUID) ([]model.Permission, error)
}

func (f *fakeUserRepo) GetPermissions(_ context.Context, id uuid.UUID) ([]model.Permission, error) {
	if f.permsFn == nil {
		f.t.Fatal("unexpected call to GetPermissions")
	}
	return f.permsFn(id)
}

func (f *fakeUserRepo) Create(_ context.Context, u model.User, roles []string) (*model.User, error) {
	if f.createFn == nil {
		f.t.Fatal("unexpected call to Create")
	}
	return f.createFn(u, roles)
}

func (f *fakeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	if f.getByIDFn == nil {
		f.t.Fatal("unexpected call to GetByID")
	}
	return f.getByIDFn(id)
}

// fakeHasher is instant and makes the "hash" easy to recognize in assertions.
type fakeHasher struct{ err error }

func (f fakeHasher) Hash(pw string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "hashed:" + pw, nil
}

// validCompanyUser returns valid input; each test changes one thing.
func validCompanyUser() CreateUserInput {
	companyID := uuid.New()
	return CreateUserInput{
		CompanyID: &companyID,
		Email:     "jane@acme.com",
		Password:  "correct horse",
		FirstName: "Jane",
		LastName:  "Smith",
		Roles:     []string{model.RoleDispatcher},
	}
}

// echoCreate is a createFn that records its input and returns it as saved.
func echoCreate(gotUser *model.User, gotRoles *[]string) func(model.User, []string) (*model.User, error) {
	return func(u model.User, roles []string) (*model.User, error) {
		*gotUser, *gotRoles = u, roles
		u.ID = uuid.New()
		return &u, nil
	}
}

func TestUserService_Create_NormalizesAndHashes(t *testing.T) {
	var saved model.User
	var roles []string
	repo := &fakeUserRepo{t: t, createFn: echoCreate(&saved, &roles)}
	svc := NewUserService(repo, fakeHasher{})

	in := validCompanyUser()
	in.Email = "  Jane@ACME.com "
	in.Username = " Jane.S "
	in.Password = " pass with spaces "
	in.FirstName = " Jane "
	in.Roles = []string{model.RoleDispatcher, model.RoleManager, model.RoleDispatcher}

	_, err := svc.Create(context.Background(), in)

	require.NoError(t, err)
	assert.Equal(t, "jane@acme.com", *saved.Email, "email lowercased and trimmed")
	assert.Equal(t, "jane.s", *saved.Username, "username lowercased and trimmed")
	assert.Equal(t, "Jane", saved.FirstName)
	assert.Equal(t, "hashed: pass with spaces ", saved.PasswordHash,
		"the hash is stored, and the password itself is not trimmed")
	assert.Equal(t, []string{model.RoleDispatcher, model.RoleManager}, roles, "duplicate roles removed")
}

func TestUserService_Create_UsernameOnlyDriver(t *testing.T) {
	var saved model.User
	var roles []string
	repo := &fakeUserRepo{t: t, createFn: echoCreate(&saved, &roles)}

	in := validCompanyUser()
	in.Email = ""
	in.Username = "driver.bob"
	in.Roles = []string{model.RoleDriver}

	_, err := NewUserService(repo, fakeHasher{}).Create(context.Background(), in)

	require.NoError(t, err)
	assert.Nil(t, saved.Email, "empty email is stored as NULL, not as \"\"")
	assert.Equal(t, "driver.bob", *saved.Username)
}

func TestUserService_Create_Invalid(t *testing.T) {
	tests := []struct {
		name      string
		modify    func(in *CreateUserInput)
		wantField string
	}{
		{"no email and no username", func(in *CreateUserInput) { in.Email = ""; in.Username = "" }, "login"},
		{"bad email", func(in *CreateUserInput) { in.Email = "not-an-email" }, "email"},
		{"email with display name", func(in *CreateUserInput) { in.Email = "Jane <jane@acme.com>" }, "email"},
		{"email without dot in domain", func(in *CreateUserInput) { in.Email = "jane@localhost" }, "email"},
		{"username too short", func(in *CreateUserInput) { in.Username = "ab" }, "username"},
		{"username with @", func(in *CreateUserInput) { in.Username = "bob@acme" }, "username"},
		{"password too short", func(in *CreateUserInput) { in.Password = "1234567" }, "password"},
		{"password over 72 bytes", func(in *CreateUserInput) { in.Password = strings.Repeat("a", 73) }, "password"},
		{"blank first name", func(in *CreateUserInput) { in.FirstName = "  " }, "first_name"},
		{"blank last name", func(in *CreateUserInput) { in.LastName = "" }, "last_name"},
		{"no roles", func(in *CreateUserInput) { in.Roles = nil }, "roles"},
		{"company user gets platform role", func(in *CreateUserInput) { in.Roles = []string{model.RolePlatformAdmin} }, "roles"},
		{"unknown role", func(in *CreateUserInput) { in.Roles = []string{"superuser"} }, "roles"},
		{"platform user gets company role", func(in *CreateUserInput) { in.CompanyID = nil; in.Roles = []string{model.RoleAdmin} }, "roles"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validCompanyUser()
			tt.modify(&in)
			// No createFn, and a hasher that fails: validation must stop
			// everything before hashing or saving.
			svc := NewUserService(&fakeUserRepo{t: t}, fakeHasher{err: errors.New("must not hash")})

			_, err := svc.Create(context.Background(), in)

			requireValidationField(t, err, tt.wantField)
		})
	}
}

func TestUserService_Create_PlatformAdmin(t *testing.T) {
	var saved model.User
	var roles []string
	repo := &fakeUserRepo{t: t, createFn: echoCreate(&saved, &roles)}

	in := validCompanyUser()
	in.CompanyID = nil
	in.Roles = []string{model.RolePlatformAdmin}

	u, err := NewUserService(repo, fakeHasher{}).Create(context.Background(), in)

	require.NoError(t, err)
	assert.True(t, u.IsPlatformUser())
}

func TestUserService_Create_Errors(t *testing.T) {
	t.Run("hasher failure", func(t *testing.T) {
		svc := NewUserService(&fakeUserRepo{t: t}, fakeHasher{err: errors.New("boom")})

		_, err := svc.Create(context.Background(), validCompanyUser())

		assert.ErrorContains(t, err, "hash password")
	})

	t.Run("repository error is returned", func(t *testing.T) {
		repo := &fakeUserRepo{t: t, createFn: func(model.User, []string) (*model.User, error) {
			return nil, model.ErrEmailTaken
		}}

		_, err := NewUserService(repo, fakeHasher{}).Create(context.Background(), validCompanyUser())

		assert.ErrorIs(t, err, model.ErrEmailTaken)
	})
}

func TestUserService_GetByID(t *testing.T) {
	repo := &fakeUserRepo{t: t, getByIDFn: func(uuid.UUID) (*model.User, error) {
		return nil, model.ErrNotFound
	}}

	_, err := NewUserService(repo, fakeHasher{}).GetByID(context.Background(), uuid.New())

	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestUserService_GetProfile(t *testing.T) {
	user := &model.User{ID: uuid.New(), FirstName: "Jane"}
	perms := []model.Permission{model.PermTruckRead, model.PermDriverRead}

	t.Run("user with permissions", func(t *testing.T) {
		repo := &fakeUserRepo{
			t:         t,
			getByIDFn: func(uuid.UUID) (*model.User, error) { return user, nil },
			permsFn:   func(uuid.UUID) ([]model.Permission, error) { return perms, nil },
		}

		p, err := NewUserService(repo, fakeHasher{}).GetProfile(context.Background(), user.ID)

		require.NoError(t, err)
		assert.Equal(t, user, p.User)
		assert.Equal(t, perms, p.Permissions)
	})

	t.Run("deleted user is not found", func(t *testing.T) {
		repo := &fakeUserRepo{t: t, getByIDFn: func(uuid.UUID) (*model.User, error) {
			return nil, model.ErrNotFound
		}} // permsFn unset: must not be called

		_, err := NewUserService(repo, fakeHasher{}).GetProfile(context.Background(), user.ID)

		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}
