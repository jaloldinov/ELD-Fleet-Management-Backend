package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

var licenseExpiry = time.Date(2028, 5, 31, 0, 0, 0, 0, time.UTC)

type driverFixture struct {
	db      *pgxpool.Pool
	tx      *TxManager
	users   *UserRepository
	drivers *DriverRepository
	alpha   *model.Company
	bravo   *model.Company
	n       int
}

func newDriverFixture(t *testing.T) *driverFixture {
	t.Helper()
	db := newTestDB(t)
	companies := NewCompanyRepository(db)
	return &driverFixture{
		db: db, tx: NewTxManager(db),
		users: NewUserRepository(db), drivers: NewDriverRepository(db),
		alpha: createCompany(t, companies, "Alpha", "1"),
		bravo: createCompany(t, companies, "Bravo", "2"),
	}
}

// createDriver creates user + driver profile in ONE transaction, the way the
// service does.
func (f *driverFixture) createDriver(ctx context.Context, companyID uuid.UUID, state, license string) (uuid.UUID, error) {
	f.n++
	var driverID uuid.UUID
	err := f.tx.WithinTx(ctx, func(ctx context.Context) error {
		u := newUser(companyID, fmt.Sprintf("driver%d@x.com", f.n))
		created, err := f.users.Create(ctx, u, []string{model.RoleDriver})
		if err != nil {
			return err
		}
		driverID, err = f.drivers.Create(ctx, model.Driver{
			UserID: created.ID, CompanyID: companyID,
			LicenseNumber: license, LicenseState: state, LicenseExpiresAt: licenseExpiry,
		})
		return err
	})
	return driverID, err
}

func TestDriverRepository_CreateAndGet(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()

	id, err := f.createDriver(ctx, f.alpha.ID, "TX", "D1234567")
	require.NoError(t, err)

	got, err := f.drivers.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "TX", got.Driver.LicenseState)
	assert.Equal(t, "D1234567", got.Driver.LicenseNumber)
	assert.Equal(t, "2028-05-31", got.Driver.LicenseExpiresAt.Format(time.DateOnly), "DATE round-trips as a calendar day")
	assert.Equal(t, f.alpha.ID, got.Driver.CompanyID)
	assert.Equal(t, "Jane", got.User.FirstName, "user fields come from the join")
	assert.True(t, got.User.IsActive)

	byUser, err := f.drivers.GetByUserID(ctx, got.Driver.UserID)
	require.NoError(t, err)
	assert.Equal(t, id, byUser.Driver.ID)
}

// The reason service-level transactions exist: if the driver insert fails,
// the user created just before it must disappear too.
func TestDriverRepository_DuplicateLicenseRollsBackUser(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	_, err := f.createDriver(ctx, f.alpha.ID, "TX", "D1234567")
	require.NoError(t, err)
	usersBefore := countUsers(t, f.db)

	_, err = f.createDriver(ctx, f.bravo.ID, "TX", "d1234567") // same state, other case

	assert.ErrorIs(t, err, model.ErrLicenseTaken)
	assert.Equal(t, usersBefore, countUsers(t, f.db), "no orphan user with a driver role")
}

func TestDriverRepository_SameLicenseOtherStateIsFine(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	_, err := f.createDriver(ctx, f.alpha.ID, "TX", "D1234567")
	require.NoError(t, err)

	_, err = f.createDriver(ctx, f.alpha.ID, "OK", "D1234567")

	assert.NoError(t, err)
}

func TestDriverRepository_CompanyMustMatchUser(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	u, err := f.users.Create(ctx, newUser(f.alpha.ID, "amy@alpha.com"), []string{model.RoleDriver})
	require.NoError(t, err)

	// The user belongs to Alpha; the profile claims Bravo.
	_, err = f.drivers.Create(ctx, model.Driver{
		UserID: u.ID, CompanyID: f.bravo.ID,
		LicenseNumber: "X1", LicenseState: "CA", LicenseExpiresAt: licenseExpiry,
	})

	assert.ErrorContains(t, err, "drivers_user_company_fkey", "the database refuses a cross-tenant driver")
}

func TestDriverRepository_List(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	for i, c := range []uuid.UUID{f.alpha.ID, f.alpha.ID, f.bravo.ID} {
		_, err := f.createDriver(ctx, c, "TX", fmt.Sprintf("L%d", i))
		require.NoError(t, err)
	}

	alphaOnly, total, err := f.drivers.List(ctx, model.DriverFilter{CompanyID: &f.alpha.ID}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	for _, d := range alphaOnly {
		assert.Equal(t, f.alpha.ID, d.Driver.CompanyID)
	}

	_, all, err := f.drivers.List(ctx, model.DriverFilter{}, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, all)
}

func TestDriverRepository_UpdateAndDelete(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	id, err := f.createDriver(ctx, f.alpha.ID, "TX", "D1")
	require.NoError(t, err)
	_, err = f.createDriver(ctx, f.alpha.ID, "CA", "TAKEN")
	require.NoError(t, err)

	t.Run("update license", func(t *testing.T) {
		newExpiry := time.Date(2030, 1, 15, 0, 0, 0, 0, time.UTC)
		require.NoError(t, f.drivers.UpdateLicense(ctx, model.Driver{
			ID: id, LicenseNumber: "N2", LicenseState: "NM", LicenseExpiresAt: newExpiry,
		}))
		got, err := f.drivers.GetByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, "NM", got.Driver.LicenseState)
		assert.Equal(t, "2030-01-15", got.Driver.LicenseExpiresAt.Format(time.DateOnly))
	})

	t.Run("update to a taken license", func(t *testing.T) {
		err := f.drivers.UpdateLicense(ctx, model.Driver{
			ID: id, LicenseNumber: "taken", LicenseState: "CA", LicenseExpiresAt: licenseExpiry,
		})
		assert.ErrorIs(t, err, model.ErrLicenseTaken)
	})

	t.Run("soft delete", func(t *testing.T) {
		require.NoError(t, f.drivers.SoftDelete(ctx, id))
		_, err := f.drivers.GetByID(ctx, id)
		assert.ErrorIs(t, err, model.ErrNotFound)
		assert.ErrorIs(t, f.drivers.SoftDelete(ctx, id), model.ErrNotFound)
	})

	t.Run("unknown driver", func(t *testing.T) {
		err := f.drivers.UpdateLicense(ctx, model.Driver{ID: uuid.New(), LicenseNumber: "X", LicenseState: "TX", LicenseExpiresAt: licenseExpiry})
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestDriverRepository_DeletedUserHidesDriver(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	id, err := f.createDriver(ctx, f.alpha.ID, "TX", "D1")
	require.NoError(t, err)
	got, err := f.drivers.GetByID(ctx, id)
	require.NoError(t, err)

	require.NoError(t, f.users.SoftDelete(ctx, got.Driver.UserID))

	_, err = f.drivers.GetByID(ctx, id)
	assert.ErrorIs(t, err, model.ErrNotFound, "a driver whose user is deleted is not shown")
}

func TestDriverRepository_Dispatcher(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	ali, err := f.createDriver(ctx, f.alpha.ID, "TX", "L1")
	require.NoError(t, err)
	bob, err := f.createDriver(ctx, f.alpha.ID, "TX", "L2")
	require.NoError(t, err)
	disp, err := f.users.Create(ctx, newUser(f.alpha.ID, "disp@alpha.com"), []string{model.RoleDispatcher})
	require.NoError(t, err)
	stranger, err := f.users.Create(ctx, newUser(f.bravo.ID, "disp@bravo.com"), []string{model.RoleDispatcher})
	require.NoError(t, err)

	require.NoError(t, f.drivers.SetDispatcher(ctx, ali, &disp.ID))

	got, err := f.drivers.GetByID(ctx, ali)
	require.NoError(t, err)
	require.NotNil(t, got.Driver.DispatcherID)
	assert.Equal(t, disp.ID, *got.Driver.DispatcherID)

	mine, total, err := f.drivers.List(ctx, model.DriverFilter{CompanyID: &f.alpha.ID, DispatcherID: &disp.ID}, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total, `"my drivers"`)
	assert.Equal(t, ali, mine[0].Driver.ID)

	t.Run("a dispatcher of another company is refused by the database", func(t *testing.T) {
		err := f.drivers.SetDispatcher(ctx, bob, &stranger.ID)
		assert.ErrorContains(t, err, "drivers_dispatcher_company_fkey")
	})

	t.Run("a driver is not their own dispatcher", func(t *testing.T) {
		b, err := f.drivers.GetByID(ctx, bob)
		require.NoError(t, err)
		err = f.drivers.SetDispatcher(ctx, bob, &b.User.ID)
		assert.ErrorContains(t, err, "drivers_dispatcher_not_self")
	})

	t.Run("unknown driver", func(t *testing.T) {
		assert.ErrorIs(t, f.drivers.SetDispatcher(ctx, uuid.New(), nil), model.ErrNotFound)
	})

	t.Run("a leaving dispatcher's drivers are unlinked", func(t *testing.T) {
		require.NoError(t, f.drivers.SetDispatcher(ctx, bob, &disp.ID))

		cleared, err := f.drivers.ClearDispatcher(ctx, disp.ID)

		require.NoError(t, err)
		assert.Len(t, cleared, 2)
		_, total, err := f.drivers.List(ctx, model.DriverFilter{DispatcherID: &disp.ID}, 20, 0)
		require.NoError(t, err)
		assert.Zero(t, total)
	})
}

func TestUserRepository_LockForShare(t *testing.T) {
	f := newDriverFixture(t)
	ctx := context.Background()
	u, err := f.users.Create(ctx, newUser(f.alpha.ID, "disp@alpha.com"), []string{model.RoleDispatcher})
	require.NoError(t, err)

	_, err = f.users.LockForShare(ctx, u.ID)
	assert.ErrorIs(t, err, errNoTx)

	err = f.tx.WithinTx(ctx, func(ctx context.Context) error {
		got, err := f.users.LockForShare(ctx, u.ID)
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)
		_, err = f.users.LockForShare(ctx, uuid.New())
		assert.ErrorIs(t, err, model.ErrNotFound)
		return nil
	})
	require.NoError(t, err)
}
