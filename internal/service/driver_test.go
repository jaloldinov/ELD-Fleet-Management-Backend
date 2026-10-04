package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// fakeTx runs fn directly but marks the ctx, so fakes can check that they
// were called INSIDE a transaction. Real commit/rollback is proven by the
// repository integration tests.
type fakeTx struct{ calls int }

type inTxKey struct{}

func (f *fakeTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	f.calls++
	return fn(context.WithValue(ctx, inTxKey{}, true))
}

func inTx(ctx context.Context) bool { v, _ := ctx.Value(inTxKey{}).(bool); return v }

// fakeDriverUsers stands in for UserService.
type fakeDriverUsers struct {
	gotCreate     CreateUserInput
	createdInTx   bool
	createErr     error
	deletedUserID uuid.UUID
	deletedInTx   bool
}

func (f *fakeDriverUsers) Create(ctx context.Context, p auth.Principal, in CreateUserInput) (*UserDetails, error) {
	f.gotCreate, f.createdInTx = in, inTx(ctx)
	if f.createErr != nil {
		return nil, f.createErr
	}
	companyID := in.CompanyID
	if !p.IsPlatformUser() {
		companyID = p.CompanyID // like the real UserService
	}
	return &UserDetails{User: &model.User{ID: uuid.New(), CompanyID: companyID}, Roles: in.Roles}, nil
}

func (f *fakeDriverUsers) Delete(ctx context.Context, _ auth.Principal, id uuid.UUID) error {
	f.deletedUserID, f.deletedInTx = id, inTx(ctx)
	return nil
}

// memDriverRepo is an in-memory driver repository.
type memDriverRepo struct {
	drivers     map[uuid.UUID]*model.DriverDetails
	createErr   error
	createdInTx bool
	deletedInTx bool
	listF       model.DriverFilter
}

func newMemDriverRepo() *memDriverRepo {
	return &memDriverRepo{drivers: map[uuid.UUID]*model.DriverDetails{}}
}

func (m *memDriverRepo) add(companyID uuid.UUID) *model.DriverDetails {
	dd := &model.DriverDetails{Driver: model.Driver{
		ID: uuid.New(), UserID: uuid.New(), CompanyID: companyID,
		LicenseNumber: "D1", LicenseState: "TX", LicenseExpiresAt: expiry,
	}}
	m.drivers[dd.Driver.ID] = dd
	return dd
}

func (m *memDriverRepo) Create(ctx context.Context, d model.Driver) (uuid.UUID, error) {
	m.createdInTx = inTx(ctx)
	if m.createErr != nil {
		return uuid.Nil, m.createErr
	}
	d.ID = uuid.New()
	m.drivers[d.ID] = &model.DriverDetails{Driver: d}
	return d.ID, nil
}

func (m *memDriverRepo) GetByID(_ context.Context, id uuid.UUID) (*model.DriverDetails, error) {
	dd, ok := m.drivers[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *dd
	return &c, nil
}

func (m *memDriverRepo) GetByUserID(_ context.Context, userID uuid.UUID) (*model.DriverDetails, error) {
	for _, dd := range m.drivers {
		if dd.Driver.UserID == userID {
			return dd, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memDriverRepo) List(_ context.Context, f model.DriverFilter, _, _ int) ([]model.DriverDetails, int, error) {
	m.listF = f
	var out []model.DriverDetails
	for _, dd := range m.drivers {
		if f.CompanyID == nil || dd.Driver.CompanyID == *f.CompanyID {
			out = append(out, *dd)
		}
	}
	return out, len(out), nil
}

func (m *memDriverRepo) UpdateLicense(_ context.Context, d model.Driver) error {
	m.drivers[d.ID].Driver = d
	return nil
}

func (m *memDriverRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	m.deletedInTx = inTx(ctx)
	delete(m.drivers, id)
	return nil
}

var expiry = time.Date(2028, 5, 31, 0, 0, 0, 0, time.UTC)

type driverSvcFixture struct {
	svc   *DriverService
	tx    *fakeTx
	users *fakeDriverUsers
	repo  *memDriverRepo
	audit *memAuditStore
}

func newDriverSvcFixture() *driverSvcFixture {
	f := &driverSvcFixture{tx: &fakeTx{}, users: &fakeDriverUsers{}, repo: newMemDriverRepo(), audit: &memAuditStore{}}
	f.svc = NewDriverService(f.tx, f.users, f.repo, NewAuditor(f.tx, f.audit))
	return f
}

func validDriverInput() CreateDriverInput {
	return CreateDriverInput{
		Username: "driver.bob", Password: "correct horse", FirstName: "Bob", LastName: "Lee",
		LicenseNumber: " d1234567 ", LicenseState: " tx ", LicenseExpiresAt: expiry,
	}
}

func TestDriverService_Create_UserAndProfileInOneTransaction(t *testing.T) {
	f := newDriverSvcFixture()

	dd, err := f.svc.Create(context.Background(), companyAUser, validDriverInput())

	require.NoError(t, err)
	assert.Equal(t, 1, f.tx.calls)
	assert.True(t, f.users.createdInTx, "user is created INSIDE the transaction")
	assert.True(t, f.repo.createdInTx, "driver profile is created INSIDE the same transaction")
	assert.Equal(t, []string{model.RoleDriver}, f.users.gotCreate.Roles, "always exactly the driver role")
	assert.Equal(t, companyA, dd.Driver.CompanyID, "company comes from the created user (the token)")
	assert.Equal(t, "TX", dd.Driver.LicenseState, "state normalized to upper case")
	assert.Equal(t, "d1234567", dd.Driver.LicenseNumber, "number trimmed")
}

func TestDriverService_Create_ProfileFailureIsReturned(t *testing.T) {
	f := newDriverSvcFixture()
	f.repo.createErr = model.ErrLicenseTaken

	_, err := f.svc.Create(context.Background(), companyAUser, validDriverInput())

	// The real TxManager rolls the user back (see repository tests); here we
	// check that the error leaves the transaction function so that happens.
	assert.ErrorIs(t, err, model.ErrLicenseTaken)
}

func TestDriverService_Create_UserErrorStopsBeforeProfile(t *testing.T) {
	f := newDriverSvcFixture()
	f.users.createErr = model.ErrUsernameTaken

	_, err := f.svc.Create(context.Background(), companyAUser, validDriverInput())

	assert.ErrorIs(t, err, model.ErrUsernameTaken)
	assert.Empty(t, f.repo.drivers, "no profile without a user")
}

func TestDriverService_Create_Invalid(t *testing.T) {
	tests := []struct {
		name      string
		principal auth.Principal
		modify    func(in *CreateDriverInput)
		field     string
	}{
		{"platform admin must choose a company", platformAdmin, func(*CreateDriverInput) {}, "company_id"},
		{"unknown state", companyAUser, func(in *CreateDriverInput) { in.LicenseState = "XX" }, "license_state"},
		{"bad license chars", companyAUser, func(in *CreateDriverInput) { in.LicenseNumber = "D 12/34" }, "license_number"},
		{"missing expiry", companyAUser, func(in *CreateDriverInput) { in.LicenseExpiresAt = time.Time{} }, "license_expires_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDriverSvcFixture()
			in := validDriverInput()
			tt.modify(&in)

			_, err := f.svc.Create(context.Background(), tt.principal, in)

			requireValidationField(t, err, tt.field)
			assert.Zero(t, f.tx.calls, "invalid input never opens a transaction")
		})
	}
}

func TestDriverService_Scope(t *testing.T) {
	f := newDriverSvcFixture()
	companyB := uuid.New()
	own := f.repo.add(companyA)
	other := f.repo.add(companyB)

	_, err := f.svc.Get(context.Background(), companyAUser, own.Driver.ID)
	assert.NoError(t, err)

	_, err = f.svc.Get(context.Background(), companyAUser, other.Driver.ID)
	assert.ErrorIs(t, err, model.ErrNotFound, "other tenant's driver: 404")

	_, err = f.svc.Update(context.Background(), companyAUser, other.Driver.ID, UpdateDriverInput{LicenseState: ptr("CA")})
	assert.ErrorIs(t, err, model.ErrNotFound)

	assert.ErrorIs(t, f.svc.Delete(context.Background(), companyAUser, other.Driver.ID), model.ErrNotFound)
	assert.Contains(t, f.repo.drivers, other.Driver.ID, "not deleted")

	page, err := f.svc.List(context.Background(), companyAUser, DriverListParams{CompanyID: &companyB})
	require.NoError(t, err)
	assert.Equal(t, &companyA, f.repo.listF.CompanyID, "forced to own company")
	assert.Equal(t, 1, page.Total)

	_, err = f.svc.Get(context.Background(), platformAdmin, other.Driver.ID)
	assert.NoError(t, err, "platform users see all drivers")
}

func TestDriverService_Update(t *testing.T) {
	f := newDriverSvcFixture()
	d := f.repo.add(companyA)
	newExpiry := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := f.svc.Update(context.Background(), companyAUser, d.Driver.ID, UpdateDriverInput{
		LicenseState: ptr("ok"), LicenseExpiresAt: &newExpiry,
	})

	require.NoError(t, err)
	assert.Equal(t, "OK", got.Driver.LicenseState)
	assert.Equal(t, newExpiry, got.Driver.LicenseExpiresAt)
	assert.Equal(t, "D1", got.Driver.LicenseNumber, "unchanged")

	_, err = f.svc.Update(context.Background(), companyAUser, d.Driver.ID, UpdateDriverInput{})
	requireValidationField(t, err, "body")

	_, err = f.svc.Update(context.Background(), companyAUser, d.Driver.ID, UpdateDriverInput{LicenseState: ptr("ZZ")})
	requireValidationField(t, err, "license_state")
}

func TestDriverService_Delete_ProfileAndUserInOneTransaction(t *testing.T) {
	f := newDriverSvcFixture()
	d := f.repo.add(companyA)

	require.NoError(t, f.svc.Delete(context.Background(), companyAUser, d.Driver.ID))

	assert.NotContains(t, f.repo.drivers, d.Driver.ID)
	assert.True(t, f.repo.deletedInTx)
	assert.Equal(t, d.Driver.UserID, f.users.deletedUserID, "the driver's user account is deleted too")
	assert.True(t, f.users.deletedInTx, "in the same transaction")
}

func TestDriverService_Me(t *testing.T) {
	f := newDriverSvcFixture()
	d := f.repo.add(companyA)

	got, err := f.svc.Me(context.Background(), auth.Principal{UserID: d.Driver.UserID, CompanyID: &companyA})
	require.NoError(t, err)
	assert.Equal(t, d.Driver.ID, got.Driver.ID)

	_, err = f.svc.Me(context.Background(), companyAUser) // an admin, not a driver
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// Compile-time check: the real UserService can be used as DriverUsers.
var _ DriverUsers = (*UserService)(nil)

// ---------- audit ----------

func TestDriverService_AuditLicenseChanges(t *testing.T) {
	f := newDriverSvcFixture()
	dd, err := f.svc.Create(context.Background(), companyAUser, validDriverInput())
	require.NoError(t, err)

	created := f.audit.last(t)
	assert.Equal(t, "driver.create", created.Action)
	assert.Equal(t, "2028-05-31", created.Changes["license_expires_at"].New, "a date, in the API format")
	assert.Equal(t, dd.Driver.UserID, created.Changes["user_id"].New, "links the driver to its user")

	_, err = f.svc.Update(context.Background(), companyAUser, dd.Driver.ID, UpdateDriverInput{LicenseState: ptr("ok")})
	require.NoError(t, err)
	assert.Equal(t, model.AuditChanges{"license_state": {Old: dd.Driver.LicenseState, New: "OK"}}, f.audit.last(t).Changes)

	require.NoError(t, f.svc.Delete(context.Background(), companyAUser, dd.Driver.ID))
	assert.Equal(t, []string{"driver.create", "driver.update", "driver.delete"}, f.audit.actions())
	assert.Equal(t, &companyA, f.audit.last(t).CompanyID)
	f.audit.requireAllInTx(t)
}
