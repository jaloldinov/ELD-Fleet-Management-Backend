package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// memUserRepo is an in-memory fake: a tiny map-based "database". For a service
// whose methods make several repository calls that depend on each other
// (get → check → update roles → revoke), this is simpler and more realistic
// than stubbing every call separately.
type memUserRepo struct {
	users   map[uuid.UUID]*model.User
	roles   map[uuid.UUID][]string
	perms   []model.Permission
	created []model.User // what Create received
	listF   model.UserFilter
}

func (m *memUserRepo) UpdatePassword(_ context.Context, id uuid.UUID, hash string) error {
	u, ok := m.users[id]
	if !ok || !u.IsActive || u.DeletedAt != nil {
		return model.ErrNotFound
	}
	u.PasswordHash = hash
	return nil
}

func newMemUserRepo() *memUserRepo {
	return &memUserRepo{users: map[uuid.UUID]*model.User{}, roles: map[uuid.UUID][]string{}}
}

// add stores a user directly (test setup) and returns it.
func (m *memUserRepo) add(companyID *uuid.UUID, roles ...string) *model.User {
	u := &model.User{ID: uuid.New(), CompanyID: companyID, FirstName: "F", LastName: "L", IsActive: true}
	m.users[u.ID] = u
	m.roles[u.ID] = roles
	return u
}

func (m *memUserRepo) Create(_ context.Context, u model.User, roles []string) (*model.User, error) {
	m.created = append(m.created, u)
	u.ID = uuid.New()
	u.IsActive = true // the database default
	m.users[u.ID] = &u
	m.roles[u.ID] = roles
	return &u, nil
}

func (m *memUserRepo) GetByID(_ context.Context, id uuid.UUID) (*model.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *u // return a copy, like a real database
	return &c, nil
}

func (m *memUserRepo) List(_ context.Context, f model.UserFilter, _, _ int) ([]model.User, int, error) {
	m.listF = f
	var out []model.User
	for _, u := range m.users {
		if f.CompanyID == nil || (u.CompanyID != nil && *u.CompanyID == *f.CompanyID) {
			out = append(out, *u)
		}
	}
	return out, len(out), nil
}

func (m *memUserRepo) Update(_ context.Context, u model.User, roles []string) (*model.User, error) {
	m.users[u.ID] = &u
	if roles != nil {
		m.roles[u.ID] = roles
	}
	return &u, nil
}

func (m *memUserRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	delete(m.users, id)
	return nil
}

func (m *memUserRepo) GetRoles(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]string, error) {
	out := map[uuid.UUID][]string{}
	for _, id := range ids {
		if r, ok := m.roles[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (m *memUserRepo) GetPermissions(context.Context, uuid.UUID) ([]model.Permission, error) {
	return m.perms, nil
}

// fakeCompanies: every company exists, except missingCompany.
type fakeCompanies struct{}

var missingCompany = uuid.New()

func (fakeCompanies) GetByID(_ context.Context, id uuid.UUID) (*model.Company, error) {
	if id == missingCompany {
		return nil, model.ErrNotFound
	}
	return &model.Company{ID: id}, nil
}

type fakeRevoker struct{ revoked []uuid.UUID }

func (f *fakeRevoker) RevokeAllForUser(_ context.Context, id uuid.UUID) error {
	f.revoked = append(f.revoked, id)
	return nil
}

// fakeHasher is instant and makes the "hash" easy to recognize in assertions.
type fakeHasher struct{ err error }

func (f fakeHasher) Hash(pw string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "hashed:" + pw, nil
}

type userFixture struct {
	svc      *UserService
	repo     *memUserRepo
	revoker  *fakeRevoker
	releaser *fakeReleaser
	audit   *memAuditStore
}

func newUserFixture() *userFixture {
	f := &userFixture{repo: newMemUserRepo(), revoker: &fakeRevoker{}, releaser: &fakeReleaser{}}
	var audit *Auditor
	audit, f.audit = newTestAuditor()
	f.svc = NewUserService(f.repo, f.releaser, fakeCompanies{}, f.revoker, fakeHasher{}, audit)
	return f
}

// companyAdminOf returns a principal for an admin of the given company,
// stored in the fake repository.
func (f *userFixture) companyAdminOf(companyID uuid.UUID) auth.Principal {
	u := f.repo.add(&companyID, model.RoleAdmin)
	return auth.Principal{UserID: u.ID, CompanyID: &companyID}
}

// validInput returns valid input for a company user; tests change one thing.
func validInput(companyID *uuid.UUID) CreateUserInput {
	return CreateUserInput{
		CompanyID: companyID,
		Email:     "jane@acme.com",
		Password:  "correct horse",
		FirstName: "Jane",
		LastName:  "Smith",
		Roles:     []string{model.RoleDispatcher},
	}
}

// =====================================================================
// Create: validation (same rules as before)
// =====================================================================

func TestUserService_Create_NormalizesAndHashes(t *testing.T) {
	f := newUserFixture()
	in := validInput(&companyA)
	in.Email = "  Jane@ACME.com "
	in.Username = " Jane.S "
	in.Password = " pass with spaces "
	in.FirstName = " Jane "
	in.Roles = []string{model.RoleDispatcher, model.RoleManager, model.RoleDispatcher}

	got, err := f.svc.Create(context.Background(), platformAdmin, in)

	require.NoError(t, err)
	saved := f.repo.created[0]
	assert.Equal(t, "jane@acme.com", *saved.Email, "email lowercased and trimmed")
	assert.Equal(t, "jane.s", *saved.Username, "username lowercased and trimmed")
	assert.Equal(t, "Jane", saved.FirstName)
	assert.Equal(t, "hashed: pass with spaces ", saved.PasswordHash,
		"the hash is stored, and the password itself is not trimmed")
	assert.Equal(t, []string{model.RoleDispatcher, model.RoleManager}, f.repo.roles[got.User.ID], "duplicate roles removed")
	assert.Equal(t, []string{model.RoleDispatcher, model.RoleManager}, got.Roles, "saved roles are returned")
}

func TestUserService_Create_UsernameOnlyDriver(t *testing.T) {
	f := newUserFixture()
	in := validInput(&companyA)
	in.Email = ""
	in.Username = "driver.bob"
	in.Roles = []string{model.RoleDriver}

	_, err := f.svc.Create(context.Background(), platformAdmin, in)

	require.NoError(t, err)
	assert.Nil(t, f.repo.created[0].Email, "empty email is stored as NULL, not as \"\"")
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
		{"company does not exist", func(in *CreateUserInput) { in.CompanyID = &missingCompany }, "company_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUserFixture()
			f.svc.hasher = fakeHasher{err: errors.New("must not hash")} // validation must stop first
			in := validInput(&companyA)
			tt.modify(&in)

			_, err := f.svc.Create(context.Background(), platformAdmin, in)

			requireValidationField(t, err, tt.wantField)
			assert.Empty(t, f.repo.created, "nothing saved")
		})
	}
}

func TestUserService_Create_HasherFailure(t *testing.T) {
	f := newUserFixture()
	f.svc.hasher = fakeHasher{err: errors.New("boom")}

	_, err := f.svc.Create(context.Background(), platformAdmin, validInput(&companyA))

	assert.ErrorContains(t, err, "hash password")
}

// =====================================================================
// Create: tenant scope and privilege escalation
// =====================================================================

func TestUserService_Create_CompanyAdminCreatesInOwnCompany(t *testing.T) {
	f := newUserFixture()
	admin := f.companyAdminOf(companyA)
	in := validInput(nil) // company_id omitted

	got, err := f.svc.Create(context.Background(), admin, in)

	require.NoError(t, err)
	assert.Equal(t, companyA, *got.User.CompanyID, "the company comes from the token")
}

func TestUserService_Create_CompanyAdminCannotTargetOtherCompany(t *testing.T) {
	f := newUserFixture()
	admin := f.companyAdminOf(companyA)
	other := uuid.New()

	_, err := f.svc.Create(context.Background(), admin, validInput(&other))

	assert.ErrorIs(t, err, model.ErrNotFound)
	assert.Empty(t, f.repo.created)
}

func TestUserService_Create_CompanyAdminCannotCreatePlatformAdmin(t *testing.T) {
	f := newUserFixture()
	admin := f.companyAdminOf(companyA)
	in := validInput(nil)
	in.Roles = []string{model.RolePlatformAdmin}

	_, err := f.svc.Create(context.Background(), admin, in)

	requireValidationField(t, err, "roles")
	assert.Empty(t, f.repo.created, "privilege escalation blocked")
}

func TestUserService_Create_SystemPrincipalCreatesPlatformAdmin(t *testing.T) {
	f := newUserFixture()
	in := validInput(nil)
	in.Roles = []string{model.RolePlatformAdmin}

	got, err := f.svc.Create(context.Background(), auth.SystemPrincipal(), in)

	require.NoError(t, err)
	assert.True(t, got.User.IsPlatformUser())
}

// =====================================================================
// Get / List: tenant scope
// =====================================================================

func TestUserService_Get_Scope(t *testing.T) {
	f := newUserFixture()
	companyB := uuid.New()
	admin := f.companyAdminOf(companyA)
	colleague := f.repo.add(&companyA, model.RoleDriver)
	stranger := f.repo.add(&companyB, model.RoleDriver)
	platformUser := f.repo.add(nil, model.RolePlatformAdmin)

	got, err := f.svc.Get(context.Background(), admin, colleague.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{model.RoleDriver}, got.Roles)

	_, err = f.svc.Get(context.Background(), admin, stranger.ID)
	assert.ErrorIs(t, err, model.ErrNotFound, "other company's user: 404")

	_, err = f.svc.Get(context.Background(), admin, platformUser.ID)
	assert.ErrorIs(t, err, model.ErrNotFound, "platform staff are invisible to companies")

	_, err = f.svc.Get(context.Background(), platformAdmin, stranger.ID)
	assert.NoError(t, err, "platform users see everyone")
}

func TestUserService_List_Scope(t *testing.T) {
	f := newUserFixture()
	companyB := uuid.New()
	admin := f.companyAdminOf(companyA)
	f.repo.add(&companyA, model.RoleDriver)
	f.repo.add(&companyB, model.RoleDriver)

	t.Run("company user is forced to own company, even if asking for another", func(t *testing.T) {
		page, err := f.svc.List(context.Background(), admin, UserListParams{CompanyID: &companyB})

		require.NoError(t, err)
		assert.Equal(t, &companyA, f.repo.listF.CompanyID)
		assert.Equal(t, 2, page.Total, "admin + driver of company A")
		for _, item := range page.Items {
			assert.Equal(t, companyA, *item.User.CompanyID)
			assert.NotEmpty(t, item.Roles, "roles are filled in")
		}
	})

	t.Run("platform user may filter by company", func(t *testing.T) {
		_, err := f.svc.List(context.Background(), platformAdmin, UserListParams{CompanyID: &companyB})
		require.NoError(t, err)
		assert.Equal(t, &companyB, f.repo.listF.CompanyID)
	})

	t.Run("platform user without filter sees all", func(t *testing.T) {
		page, err := f.svc.List(context.Background(), platformAdmin, UserListParams{})
		require.NoError(t, err)
		assert.Nil(t, f.repo.listF.CompanyID)
		assert.Equal(t, 3, page.Total)
	})
}

// =====================================================================
// Update / Delete: scope, self-protection, session revocation
// =====================================================================

func TestUserService_Update_ChangesRolesAndNames(t *testing.T) {
	f := newUserFixture()
	admin := f.companyAdminOf(companyA)
	u := f.repo.add(&companyA, model.RoleDriver)

	got, err := f.svc.Update(context.Background(), admin, u.ID, UpdateUserInput{
		FirstName: ptr(" Bob "),
		Roles:     &[]string{model.RoleDispatcher, model.RoleManager},
	})

	require.NoError(t, err)
	assert.Equal(t, "Bob", got.User.FirstName)
	assert.ElementsMatch(t, []string{model.RoleDispatcher, model.RoleManager}, got.Roles)
	assert.Empty(t, f.revoker.revoked, "still active: sessions untouched")
}

func TestUserService_Update_DeactivationRevokesSessions(t *testing.T) {
	f := newUserFixture()
	admin := f.companyAdminOf(companyA)
	u := f.repo.add(&companyA, model.RoleDriver)

	got, err := f.svc.Update(context.Background(), admin, u.ID, UpdateUserInput{IsActive: ptr(false)})

	require.NoError(t, err)
	assert.False(t, got.User.IsActive)
	assert.Equal(t, []uuid.UUID{u.ID}, f.revoker.revoked)
	assert.Equal(t, []uuid.UUID{u.ID}, f.releaser.users, "a disabled driver drives nothing")
	assert.True(t, f.releaser.allInTx)
}

func TestUserService_Update_OtherChangesKeepTheTruck(t *testing.T) {
	f := newUserFixture()
	admin := f.companyAdminOf(companyA)
	u := f.repo.add(&companyA, model.RoleDriver)

	_, err := f.svc.Update(context.Background(), admin, u.ID, UpdateUserInput{FirstName: ptr("Ali"), IsActive: ptr(true)})

	require.NoError(t, err)
	assert.Empty(t, f.releaser.users)
}

func TestUserService_Update_Denied(t *testing.T) {
	companyB := uuid.New()

	tests := []struct {
		name    string
		setup   func(f *userFixture) (auth.Principal, uuid.UUID)
		in      UpdateUserInput
		wantErr error
		field   string
	}{
		{
			name: "user of another company",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				return f.companyAdminOf(companyA), f.repo.add(&companyB, model.RoleDriver).ID
			},
			in: UpdateUserInput{FirstName: ptr("X")}, wantErr: model.ErrNotFound,
		},
		{
			name: "grant own roles (self-escalation)",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				p := f.companyAdminOf(companyA)
				return p, p.UserID
			},
			in: UpdateUserInput{Roles: &[]string{model.RoleAdmin, model.RoleManager}}, wantErr: model.ErrForbidden,
		},
		{
			name: "deactivate yourself (self-lockout)",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				p := f.companyAdminOf(companyA)
				return p, p.UserID
			},
			in: UpdateUserInput{IsActive: ptr(false)}, wantErr: model.ErrForbidden,
		},
		{
			name: "give a company user the platform_admin role",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				return f.companyAdminOf(companyA), f.repo.add(&companyA, model.RoleDriver).ID
			},
			in: UpdateUserInput{Roles: &[]string{model.RolePlatformAdmin}}, field: "roles",
		},
		{
			name: "even a platform admin cannot give company users platform roles",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				return platformAdmin, f.repo.add(&companyA, model.RoleDriver).ID
			},
			in: UpdateUserInput{Roles: &[]string{model.RolePlatformAdmin}}, field: "roles",
		},
		{
			name: "empty body",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				return f.companyAdminOf(companyA), f.repo.add(&companyA, model.RoleDriver).ID
			},
			in: UpdateUserInput{}, field: "body",
		},
		{
			name: "blank name",
			setup: func(f *userFixture) (auth.Principal, uuid.UUID) {
				return f.companyAdminOf(companyA), f.repo.add(&companyA, model.RoleDriver).ID
			},
			in: UpdateUserInput{LastName: ptr("  ")}, field: "last_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUserFixture()
			p, target := tt.setup(f)
			rolesBefore := slices.Clone(f.repo.roles[target])

			_, err := f.svc.Update(context.Background(), p, target, tt.in)

			if tt.field != "" {
				requireValidationField(t, err, tt.field)
			} else {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			assert.Equal(t, rolesBefore, f.repo.roles[target], "nothing changed")
			assert.Empty(t, f.revoker.revoked)
		})
	}
}

func TestUserService_Delete(t *testing.T) {
	t.Run("deletes and revokes sessions", func(t *testing.T) {
		f := newUserFixture()
		admin := f.companyAdminOf(companyA)
		u := f.repo.add(&companyA, model.RoleDriver)

		require.NoError(t, f.svc.Delete(context.Background(), admin, u.ID))

		assert.NotContains(t, f.repo.users, u.ID)
		assert.Equal(t, []uuid.UUID{u.ID}, f.revoker.revoked)
		assert.Equal(t, []uuid.UUID{u.ID}, f.releaser.users)
		assert.True(t, f.releaser.allInTx)
	})

	t.Run("cannot delete yourself", func(t *testing.T) {
		f := newUserFixture()
		admin := f.companyAdminOf(companyA)

		assert.ErrorIs(t, f.svc.Delete(context.Background(), admin, admin.UserID), model.ErrForbidden)
	})

	t.Run("other company's user is not found", func(t *testing.T) {
		f := newUserFixture()
		admin := f.companyAdminOf(companyA)
		other := uuid.New()
		u := f.repo.add(&other, model.RoleDriver)

		assert.ErrorIs(t, f.svc.Delete(context.Background(), admin, u.ID), model.ErrNotFound)
		assert.Contains(t, f.repo.users, u.ID, "not deleted")
	})
}

// =====================================================================
// Profile / permissions
// =====================================================================

func TestUserService_GetProfile(t *testing.T) {
	f := newUserFixture()
	f.repo.perms = []model.Permission{model.PermTruckRead}
	u := f.repo.add(&companyA, model.RoleDispatcher)

	p, err := f.svc.GetProfile(context.Background(), u.ID)

	require.NoError(t, err)
	assert.Equal(t, []string{model.RoleDispatcher}, p.Roles)
	assert.Equal(t, []model.Permission{model.PermTruckRead}, p.Permissions)

	u.IsActive = false
	_, err = f.svc.GetProfile(context.Background(), u.ID)
	assert.ErrorIs(t, err, model.ErrUserInactive)

	_, err = f.svc.GetProfile(context.Background(), uuid.New())
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestUserService_GetPermissions(t *testing.T) {
	f := newUserFixture()
	f.repo.perms = []model.Permission{model.PermTruckRead}

	got, err := f.svc.GetPermissions(context.Background(), uuid.New())

	require.NoError(t, err)
	assert.Equal(t, []model.Permission{model.PermTruckRead}, got)
}

// ---------- SetPassword ----------

func TestUserService_SetPassword(t *testing.T) {
	f := newUserFixture()
	companyID := uuid.New()
	admin := f.companyAdminOf(companyID)
	driver := f.repo.add(&companyID, model.RoleDriver)

	err := f.svc.SetPassword(context.Background(), admin, driver.ID, "temporary-pass-1")

	require.NoError(t, err)
	assert.Equal(t, "hashed:temporary-pass-1", f.repo.users[driver.ID].PasswordHash)
	assert.Equal(t, []uuid.UUID{driver.ID}, f.revoker.revoked, "logged out everywhere")
}

func TestUserService_SetPassword_PlatformAdminAnyCompany(t *testing.T) {
	f := newUserFixture()
	companyID := uuid.New()
	driver := f.repo.add(&companyID, model.RoleDriver)

	require.NoError(t, f.svc.SetPassword(context.Background(), platformAdmin, driver.ID, "temporary-pass-1"))
}

func TestUserService_SetPassword_Refused(t *testing.T) {
	f := newUserFixture()
	companyA, companyB := uuid.New(), uuid.New()
	admin := f.companyAdminOf(companyA)
	other := f.repo.add(&companyB, model.RoleDriver)
	disabled := f.repo.add(&companyA, model.RoleDriver)
	disabled.IsActive = false
	platformUser := f.repo.add(nil, model.RolePlatformAdmin)

	tests := []struct {
		name    string
		target  uuid.UUID
		wantErr error
	}{
		{"another company's user", other.ID, model.ErrNotFound},
		{"a platform user", platformUser.ID, model.ErrNotFound},
		{"unknown user", uuid.New(), model.ErrNotFound},
		{"own account", admin.UserID, model.ErrForbidden},
		{"disabled user", disabled.ID, model.ErrUserInactive},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := f.svc.SetPassword(context.Background(), admin, tc.target, "temporary-pass-1")

			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
	assert.Empty(t, f.revoker.revoked, "nobody was logged out")
	for _, u := range f.repo.users {
		assert.NotEqual(t, "hashed:temporary-pass-1", u.PasswordHash, "no password changed")
	}
}

func TestUserService_SetPassword_Validation(t *testing.T) {
	f := newUserFixture()
	companyID := uuid.New()
	admin := f.companyAdminOf(companyID)
	driver := f.repo.add(&companyID, model.RoleDriver)
	f.svc.hasher = fakeHasher{err: errors.New("must not hash")} // validation must stop first

	for _, pw := range []string{"", "short", strings.Repeat("x", 73)} {
		err := f.svc.SetPassword(context.Background(), admin, driver.ID, pw)

		requireValidationField(t, err, "new_password")
	}
}

// ---------- audit ----------

func TestUserService_AuditLifecycle(t *testing.T) {
	f := newUserFixture()
	companyID := uuid.New()
	admin := f.companyAdminOf(companyID)
	ctx := context.Background()

	d, err := f.svc.Create(ctx, admin, CreateUserInput{
		Username: "dan", Password: "long-enough-pw", FirstName: "Dan", LastName: "Driver",
		Roles: []string{model.RoleDispatcher},
	})
	require.NoError(t, err)
	created := f.audit.last(t)
	assert.Equal(t, "user.create", created.Action)
	assert.Equal(t, &companyID, created.CompanyID)
	assert.Equal(t, []string{"dispatcher"}, created.Changes["roles"].New)
	for field := range created.Changes {
		assert.NotContains(t, field, "password", "never the password or its hash")
	}

	_, err = f.svc.Update(ctx, admin, d.User.ID, UpdateUserInput{
		Roles: &[]string{model.RoleManager, model.RoleDispatcher}, IsActive: ptr(false),
	})
	require.NoError(t, err)
	assert.Equal(t, model.AuditChanges{
		"roles":     {Old: []string{"dispatcher"}, New: []string{"dispatcher", "manager"}},
		"is_active": {Old: true, New: false},
	}, f.audit.last(t).Changes)

	require.NoError(t, f.svc.Delete(ctx, admin, d.User.ID))

	assert.Equal(t, []string{"user.create", "user.update", "user.delete"}, f.audit.actions())
	f.audit.requireAllInTx(t)
}

func TestUserService_AuditSameRolesInOtherOrderIsNoChange(t *testing.T) {
	f := newUserFixture()
	companyID := uuid.New()
	admin := f.companyAdminOf(companyID)
	u := f.repo.add(&companyID, model.RoleManager, model.RoleDispatcher)

	_, err := f.svc.Update(context.Background(), admin, u.ID, UpdateUserInput{
		Roles: &[]string{model.RoleDispatcher, model.RoleManager},
	})

	require.NoError(t, err)
	assert.Empty(t, f.audit.entries)
}

func TestUserService_AuditSetPasswordWithoutSecrets(t *testing.T) {
	f := newUserFixture()
	companyID := uuid.New()
	admin := f.companyAdminOf(companyID)
	driver := f.repo.add(&companyID, model.RoleDriver)

	require.NoError(t, f.svc.SetPassword(context.Background(), admin, driver.ID, "temporary-pass-1"))

	e := f.audit.last(t)
	assert.Equal(t, "user.set_password", e.Action)
	assert.Equal(t, driver.ID.String(), e.EntityID)
	assert.Nil(t, e.Changes, "the action alone, no values")
	f.audit.requireAllInTx(t)
}
