package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// memAssignments is an in-memory stint store with the same two rules as the
// database's partial unique indexes.
type memAssignments struct {
	open        map[uuid.UUID]*model.DriverAssignment // by driver
	ended       []model.DriverAssignment
	userDrivers map[uuid.UUID]uuid.UUID // user id → driver id
	names       map[uuid.UUID]string    // driver id → first name, for ListCrew
	historyF    model.DriverAssignmentFilter
	historyPage [2]int // limit, offset
	readOutside int    // ListCrew calls outside a snapshot
	// endedMeanwhile: EndOpenByDriver finds nothing, as when the old truck
	// was deleted (ending its stints) while we waited for its lock.
	endedMeanwhile bool
}

func newMemAssignments() *memAssignments {
	return &memAssignments{open: map[uuid.UUID]*model.DriverAssignment{},
		userDrivers: map[uuid.UUID]uuid.UUID{}, names: map[uuid.UUID]string{}}
}

func (m *memAssignments) Open(_ context.Context, a model.DriverAssignment) (*model.DriverAssignment, error) {
	if _, ok := m.open[a.DriverID]; ok {
		return nil, model.ErrDriverOnTruck
	}
	for _, o := range m.open {
		if o.TruckID == a.TruckID && o.Role == a.Role {
			return nil, model.ErrSeatTaken
		}
	}
	a.ID = uuid.New()
	m.open[a.DriverID] = &a
	c := a
	return &c, nil
}

func (m *memAssignments) GetOpenByDriver(_ context.Context, driverID uuid.UUID) (*model.DriverAssignment, error) {
	a, ok := m.open[driverID]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *a
	return &c, nil
}

func (m *memAssignments) EndOpenByDriver(_ context.Context, driverID uuid.UUID, endedBy *uuid.UUID) (*model.DriverAssignment, error) {
	if m.endedMeanwhile {
		delete(m.open, driverID)
	}
	a, ok := m.open[driverID]
	if !ok {
		return nil, model.ErrNotFound
	}
	delete(m.open, driverID)
	now := a.StartedAt
	a.EndedAt, a.EndedBy = &now, endedBy
	m.ended = append(m.ended, *a)
	c := *a
	return &c, nil
}

func (m *memAssignments) EndOpenByDriverUser(ctx context.Context, userID uuid.UUID, endedBy *uuid.UUID) ([]model.DriverAssignment, error) {
	driverID, ok := m.userDrivers[userID]
	if !ok {
		return nil, nil // not a driver
	}
	a, err := m.EndOpenByDriver(ctx, driverID, endedBy)
	if errors.Is(err, model.ErrNotFound) {
		return nil, nil
	}
	return []model.DriverAssignment{*a}, err
}

func (m *memAssignments) EndOpenByTruck(ctx context.Context, truckID uuid.UUID, endedBy *uuid.UUID) ([]model.DriverAssignment, error) {
	var out []model.DriverAssignment
	for driverID, a := range m.open {
		if a.TruckID == truckID {
			ended, _ := m.EndOpenByDriver(ctx, driverID, endedBy)
			out = append(out, *ended)
		}
	}
	return out, nil
}

func (m *memAssignments) ListCrew(ctx context.Context, truckID uuid.UUID) ([]model.CrewMember, error) {
	if !inTx(ctx) {
		m.readOutside++
	}
	var crew []model.CrewMember
	for _, a := range m.open {
		if a.TruckID == truckID {
			crew = append(crew, model.CrewMember{Assignment: *a, FirstName: m.names[a.DriverID]})
		}
	}
	sort.Slice(crew, func(i, j int) bool { return crew[i].Assignment.Role == model.AssignmentPrimary })
	return crew, nil
}

func (m *memAssignments) ListHistory(_ context.Context, f model.DriverAssignmentFilter, limit, offset int) ([]model.DriverAssignmentRecord, int, error) {
	m.historyF, m.historyPage = f, [2]int{limit, offset}
	return nil, 0, nil
}

// memELDAssignments: device ↔ truck stints, with the database's two rules.
type memELDAssignments struct {
	open     map[uuid.UUID]*model.ELDAssignment // by device
	ended    []model.ELDAssignment
	historyF model.ELDAssignmentFilter
}

func newMemELDAssignments() *memELDAssignments {
	return &memELDAssignments{open: map[uuid.UUID]*model.ELDAssignment{}}
}

func (m *memELDAssignments) Open(_ context.Context, a model.ELDAssignment) (*model.ELDAssignment, error) {
	for _, o := range m.open {
		if o.TruckID == a.TruckID {
			return nil, model.ErrTruckHasELD
		}
	}
	if _, ok := m.open[a.ELDDeviceID]; ok {
		return nil, errors.New("eta_device_open_uniq")
	}
	a.ID = uuid.New()
	m.open[a.ELDDeviceID] = &a
	c := a
	return &c, nil
}

func (m *memELDAssignments) GetOpenByDevice(_ context.Context, deviceID uuid.UUID) (*model.ELDAssignment, error) {
	a, ok := m.open[deviceID]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *a
	return &c, nil
}

func (m *memELDAssignments) GetOpenByTruck(_ context.Context, truckID uuid.UUID) (*model.ELDAssignment, error) {
	for _, a := range m.open {
		if a.TruckID == truckID {
			c := *a
			return &c, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memELDAssignments) GetInstalled(ctx context.Context, truckID uuid.UUID) (*model.InstalledDevice, error) {
	a, err := m.GetOpenByTruck(ctx, truckID)
	if err != nil {
		return nil, err
	}
	return &model.InstalledDevice{Assignment: *a, SerialNumber: "PT30-0001"}, nil // memELDRepo.add's serial
}

func (m *memELDAssignments) EndOpenByDevice(_ context.Context, deviceID uuid.UUID, endedBy *uuid.UUID) (*model.ELDAssignment, error) {
	a, ok := m.open[deviceID]
	if !ok {
		return nil, model.ErrNotFound
	}
	delete(m.open, deviceID)
	now := a.StartedAt
	a.EndedAt, a.EndedBy = &now, endedBy
	m.ended = append(m.ended, *a)
	c := *a
	return &c, nil
}

func (m *memELDAssignments) EndOpenByTruck(ctx context.Context, truckID uuid.UUID, endedBy *uuid.UUID) (*model.ELDAssignment, error) {
	a, err := m.GetOpenByTruck(ctx, truckID)
	if err != nil {
		return nil, err
	}
	return m.EndOpenByDevice(ctx, a.ELDDeviceID, endedBy)
}

func (m *memELDAssignments) ListHistory(_ context.Context, f model.ELDAssignmentFilter, _, _ int) ([]model.ELDAssignmentRecord, int, error) {
	m.historyF = f
	return nil, 0, nil
}

type lockingDevices struct {
	*memELDRepo
	log *lockLog
}

func (l lockingDevices) LockByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error) {
	if !inTx(ctx) {
		return nil, errors.New("lock outside transaction")
	}
	*l.log = append(*l.log, "device")
	return l.GetByID(ctx, id)
}

// fakeReleaser stands in for AssignmentService in TruckService and
// UserService tests.
type fakeReleaser struct {
	trucks, users, devices []uuid.UUID
	driversOnly            []uuid.UUID // ReleaseDrivers: the device stays
	allInTx                bool
	calls                  int
	log                    *[]string // optional: shared event log, to check order
}

func (f *fakeReleaser) called(ctx context.Context) {
	f.allInTx = (f.calls == 0 || f.allInTx) && inTx(ctx)
	f.calls++
	if f.log != nil {
		*f.log = append(*f.log, "release")
	}
}

func (f *fakeReleaser) ReleaseTruck(ctx context.Context, _ auth.Principal, truckID uuid.UUID) error {
	f.called(ctx)
	f.trucks = append(f.trucks, truckID)
	return nil
}

func (f *fakeReleaser) ReleaseDrivers(ctx context.Context, _ auth.Principal, truckID uuid.UUID) error {
	f.called(ctx)
	f.driversOnly = append(f.driversOnly, truckID)
	return nil
}

func (f *fakeReleaser) ReleaseDevice(ctx context.Context, _ auth.Principal, deviceID uuid.UUID) error {
	f.called(ctx)
	f.devices = append(f.devices, deviceID)
	return nil
}

func (f *fakeReleaser) ReleaseUser(ctx context.Context, _ auth.Principal, userID uuid.UUID) error {
	f.called(ctx)
	f.users = append(f.users, userID)
	return nil
}

// lockLog records which rows were locked, in order. Locking outside a
// transaction fails, like the real repositories.
type lockLog []string

type lockingDrivers struct {
	*memDriverRepo
	log *lockLog
}

func (l lockingDrivers) LockByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error) {
	if !inTx(ctx) {
		return nil, errors.New("lock outside transaction")
	}
	*l.log = append(*l.log, "driver")
	return l.GetByID(ctx, id)
}

type lockingTrucks struct {
	*memTruckRepo
	log *lockLog
}

func (l lockingTrucks) LockByID(ctx context.Context, id uuid.UUID) (*model.Truck, error) {
	if !inTx(ctx) {
		return nil, errors.New("lock outside transaction")
	}
	*l.log = append(*l.log, "truck")
	return l.GetByID(ctx, id)
}

// lockingUsers: users who may (or may not) dispatch.
type lockingUsers struct {
	users map[uuid.UUID]*model.User
	perms map[uuid.UUID][]model.Permission
	log   *lockLog
}

func (l lockingUsers) LockForShare(ctx context.Context, id uuid.UUID) (*model.User, error) {
	if !inTx(ctx) {
		return nil, errors.New("lock outside transaction")
	}
	*l.log = append(*l.log, "user")
	u, ok := l.users[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	c := *u
	return &c, nil
}

func (l lockingUsers) GetPermissions(_ context.Context, id uuid.UUID) ([]model.Permission, error) {
	return l.perms[id], nil
}

type assignmentSvcFixture struct {
	svc         *AssignmentService
	assignments *memAssignments
	elds        *memELDAssignments
	drivers     *memDriverRepo
	devices     *memELDRepo
	trucks      *memTruckRepo
	audit       *memAuditStore
	locks       lockLog
	users       lockingUsers
}

func newAssignmentSvcFixture() *assignmentSvcFixture {
	f := &assignmentSvcFixture{assignments: newMemAssignments(), elds: newMemELDAssignments(),
		drivers: newMemDriverRepo(), devices: newMemELDRepo(), trucks: newMemTruckRepo()}
	f.users = lockingUsers{users: map[uuid.UUID]*model.User{}, perms: map[uuid.UUID][]model.Permission{}, log: &f.locks}
	var audit *Auditor
	audit, f.audit = newTestAuditor()
	f.svc = NewAssignmentService(AssignmentDeps{
		Tx:           &fakeTx{},
		DriverStints: f.assignments,
		ELDStints:    f.elds,
		Drivers:      lockingDrivers{f.drivers, &f.locks},
		Devices:      lockingDevices{f.devices, &f.locks},
		Trucks:       lockingTrucks{f.trucks, &f.locks},
		Users:        f.users,
		Audit:        audit,
	})
	return f
}

// dispatcher adds an active user of the company who may dispatch.
func (f *assignmentSvcFixture) dispatcher(companyID uuid.UUID) *model.User {
	cid := companyID
	u := &model.User{ID: uuid.New(), CompanyID: &cid, IsActive: true}
	f.users.users[u.ID] = u
	f.users.perms[u.ID] = []model.Permission{model.PermDriverRead, model.PermAssignmentManage}
	return u
}

// driver adds an active driver of the company.
func (f *assignmentSvcFixture) driver(companyID uuid.UUID) *model.DriverDetails {
	dd := f.drivers.add(companyID)
	dd.User.IsActive = true
	f.assignments.userDrivers[dd.Driver.UserID] = dd.Driver.ID
	f.assignments.names[dd.Driver.ID] = "Driver-" + dd.Driver.ID.String()[:4]
	return dd
}

func TestAssignmentService_AssignDriver(t *testing.T) {
	ctx := context.Background()

	t.Run("primary by default, locked driver then truck, audited", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)

		got, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})

		require.NoError(t, err)
		assert.Equal(t, model.AssignmentPrimary, got.Role)
		assert.Equal(t, tr.ID, got.TruckID)
		assert.Equal(t, companyA, got.CompanyID)
		assert.Equal(t, companyAUser.UserID, *got.StartedBy)
		assert.Equal(t, lockLog{"driver", "truck"}, f.locks, "always this order: no deadlocks")

		e := f.audit.last(t)
		assert.Equal(t, "driver.assign_truck", e.Action)
		assert.Equal(t, model.AuditDriver, e.Entity)
		assert.Equal(t, d.Driver.ID.String(), e.EntityID)
		assert.Equal(t, model.AuditChange{Old: nil, New: tr.ID}, e.Changes["truck_id"])
		assert.Equal(t, model.AuditChange{Old: nil, New: model.AssignmentPrimary}, e.Changes["role"])
		f.audit.requireAllInTx(t)
	})

	t.Run("co-driver", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		tr := f.trucks.add(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, f.driver(companyA).Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)

		got, err := f.svc.AssignDriver(ctx, companyAUser, f.driver(companyA).Driver.ID,
			AssignDriverInput{TruckID: tr.ID, Role: model.AssignmentCoDriver})

		require.NoError(t, err)
		assert.Equal(t, model.AssignmentCoDriver, got.Role)
	})

	t.Run("invalid role is refused before any lock", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)

		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: uuid.New(), Role: "passenger"})

		requireValidationField(t, err, "role")
		assert.Empty(t, f.locks)
	})

	t.Run("moving ends the old stint and opens a new one", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		t1, t2 := f.trucks.add(companyA), f.trucks.add(companyA)
		first, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: t1.ID})
		require.NoError(t, err)

		second, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: t2.ID})

		require.NoError(t, err)
		assert.Equal(t, t2.ID, second.TruckID)
		require.Len(t, f.assignments.ended, 1)
		assert.Equal(t, first.ID, f.assignments.ended[0].ID)
		assert.Equal(t, companyAUser.UserID, *f.assignments.ended[0].EndedBy)
		assert.Equal(t, model.AuditChange{Old: t1.ID, New: t2.ID}, f.audit.last(t).Changes["truck_id"])
		assert.NotContains(t, f.audit.last(t).Changes, "role", "role did not change")
	})

	t.Run("same truck, other role: moves seats", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)

		got, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID, Role: model.AssignmentCoDriver})

		require.NoError(t, err)
		assert.Equal(t, model.AssignmentCoDriver, got.Role)
		assert.NotContains(t, f.audit.last(t).Changes, "truck_id")
	})

	t.Run("asking for what is already true changes nothing", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		first, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)
		entries := len(f.audit.entries)

		again, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID, Role: model.AssignmentPrimary})

		require.NoError(t, err)
		assert.Equal(t, first.ID, again.ID, "same stint, not a new one")
		assert.Empty(t, f.assignments.ended)
		assert.Len(t, f.audit.entries, entries, "nothing recorded")
	})

	t.Run("seat taken", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		tr := f.trucks.add(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, f.driver(companyA).Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)

		_, err = f.svc.AssignDriver(ctx, companyAUser, f.driver(companyA).Driver.ID, AssignDriverInput{TruckID: tr.ID})

		assert.ErrorIs(t, err, model.ErrSeatTaken)
	})

	t.Run("other company's truck looks like a missing one", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		foreign := f.trucks.add(uuid.New())

		_, errForeign := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: foreign.ID})
		_, errMissing := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: uuid.New()})

		requireValidationField(t, errForeign, "truck_id")
		assert.Equal(t, errMissing.Error(), errForeign.Error(), "no way to probe other tenants' truck IDs")
		assert.Empty(t, f.assignments.open)
	})

	t.Run("platform admin cannot cross companies either", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		foreign := f.trucks.add(uuid.New())

		_, err := f.svc.AssignDriver(ctx, platformAdmin, d.Driver.ID, AssignDriverInput{TruckID: foreign.ID})

		requireValidationField(t, err, "truck_id")
	})

	for _, st := range []model.TruckStatus{model.TruckInMaintenance, model.TruckOutOfService} {
		t.Run("no drivers for a truck "+string(st), func(t *testing.T) {
			f := newAssignmentSvcFixture()
			d := f.driver(companyA)
			tr := f.trucks.add(companyA)
			tr.Status = st

			_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})

			var vErr *validation.ValidationError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, truckUnavailable(st), vErr.Fields["truck_id"])
		})
	}

	t.Run("disabled driver account", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		d.User.IsActive = false
		tr := f.trucks.add(companyA)

		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})

		requireValidationField(t, err, "driver")
	})

	t.Run("driver of another company does not exist", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(uuid.New())
		tr := f.trucks.add(companyA)

		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})

		assert.ErrorIs(t, err, model.ErrNotFound)
	})

	t.Run("old stint ended meanwhile: the move still succeeds", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		t1, t2 := f.trucks.add(companyA), f.trucks.add(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: t1.ID})
		require.NoError(t, err)
		f.assignments.endedMeanwhile = true // truck 1 deleted while we waited

		got, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: t2.ID})

		require.NoError(t, err)
		assert.Equal(t, t2.ID, got.TruckID)
	})

	t.Run("platform admin assigns within the driver's company", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)

		got, err := f.svc.AssignDriver(ctx, platformAdmin, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})

		require.NoError(t, err)
		assert.Equal(t, companyA, got.CompanyID)
	})
}

func TestAssignmentService_AssignMe(t *testing.T) {
	ctx := context.Background()

	t.Run("a driver picks their own truck", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		me := auth.Principal{UserID: d.Driver.UserID, CompanyID: &companyA}

		got, err := f.svc.AssignMe(ctx, me, AssignDriverInput{TruckID: tr.ID})

		require.NoError(t, err)
		assert.Equal(t, d.Driver.ID, got.DriverID)
		assert.Equal(t, d.Driver.UserID, *got.StartedBy, "self-service is visible in the history")
	})

	t.Run("a user who is not a driver", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		tr := f.trucks.add(companyA)

		_, err := f.svc.AssignMe(ctx, companyAUser, AssignDriverInput{TruckID: tr.ID})

		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestAssignmentService_Unassign(t *testing.T) {
	ctx := context.Background()

	t.Run("ends the stint and records it", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)

		require.NoError(t, f.svc.UnassignDriver(ctx, companyAUser, d.Driver.ID))

		assert.Empty(t, f.assignments.open)
		require.Len(t, f.assignments.ended, 1)
		e := f.audit.last(t)
		assert.Equal(t, "driver.unassign_truck", e.Action)
		assert.Equal(t, model.AuditChange{Old: tr.ID, New: nil}, e.Changes["truck_id"])
		f.audit.requireAllInTx(t)
	})

	t.Run("on no truck: not an error, nothing recorded", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)

		require.NoError(t, f.svc.UnassignDriver(ctx, companyAUser, d.Driver.ID))

		assert.Empty(t, f.audit.entries)
	})

	t.Run("driver of another company does not exist", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(uuid.New())

		assert.ErrorIs(t, f.svc.UnassignDriver(ctx, companyAUser, d.Driver.ID), model.ErrNotFound)
	})

	t.Run("a driver leaves their own truck", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		me := auth.Principal{UserID: d.Driver.UserID, CompanyID: &companyA}
		_, err := f.svc.AssignMe(ctx, me, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)

		require.NoError(t, f.svc.UnassignMe(ctx, me))

		assert.Empty(t, f.assignments.open)
		assert.Equal(t, d.Driver.UserID, *f.assignments.ended[0].EndedBy)
	})

	t.Run("UnassignMe for a non-driver", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		assert.ErrorIs(t, f.svc.UnassignMe(ctx, companyAUser), model.ErrNotFound)
	})
}

func TestAssignmentService_ReleaseTruck(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	tr, other := f.trucks.add(companyA), f.trucks.add(companyA)
	ali, bob, cat := f.driver(companyA), f.driver(companyA), f.driver(companyA)
	_, err := f.svc.AssignDriver(ctx, companyAUser, ali.Driver.ID, AssignDriverInput{TruckID: tr.ID})
	require.NoError(t, err)
	_, err = f.svc.AssignDriver(ctx, companyAUser, bob.Driver.ID, AssignDriverInput{TruckID: tr.ID, Role: model.AssignmentCoDriver})
	require.NoError(t, err)
	_, err = f.svc.AssignDriver(ctx, companyAUser, cat.Driver.ID, AssignDriverInput{TruckID: other.ID})
	require.NoError(t, err)
	before := len(f.audit.entries)

	require.NoError(t, f.svc.ReleaseTruck(ctx, companyAUser, tr.ID))

	assert.Len(t, f.assignments.ended, 2, "both drivers of the truck")
	assert.Contains(t, f.assignments.open, cat.Driver.ID, "other trucks are untouched")
	released := f.audit.entries[before:]
	require.Len(t, released, 2, "one entry per driver")
	for _, e := range released {
		assert.Equal(t, "driver.unassign_truck", e.Action)
		assert.Equal(t, model.AuditDriver, e.Entity)
		assert.Equal(t, model.AuditChange{Old: tr.ID, New: nil}, e.Changes["truck_id"])
	}

	require.NoError(t, f.svc.ReleaseTruck(ctx, companyAUser, tr.ID), "nobody left: not an error")
}

func TestAssignmentService_ReleaseUser(t *testing.T) {
	ctx := context.Background()

	t.Run("a driver's account", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)

		require.NoError(t, f.svc.ReleaseUser(ctx, companyAUser, d.Driver.UserID))

		assert.Empty(t, f.assignments.open)
		assert.Equal(t, companyAUser.UserID, *f.assignments.ended[0].EndedBy)
		assert.Equal(t, "driver.unassign_truck", f.audit.last(t).Action)
	})

	t.Run("not a driver, or on no truck: nothing to do", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)

		require.NoError(t, f.svc.ReleaseUser(ctx, companyAUser, uuid.New()))
		require.NoError(t, f.svc.ReleaseUser(ctx, companyAUser, d.Driver.UserID))

		assert.Empty(t, f.audit.entries)
	})
}

// ---------- ELD devices ----------

func TestAssignmentService_InstallDevice(t *testing.T) {
	ctx := context.Background()

	t.Run("installs, locking device then truck", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		tr := f.trucks.add(companyA)

		got, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

		require.NoError(t, err)
		assert.Equal(t, tr.ID, *got.TruckID)
		assert.Equal(t, lockLog{"device", "truck"}, f.locks)
		require.Contains(t, f.elds.open, d.ID)
		assert.Equal(t, companyAUser.UserID, *f.elds.open[d.ID].StartedBy)
	})

	t.Run("audit: install, move, remove", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		first, second := f.trucks.add(companyA), f.trucks.add(companyA)

		_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, first.ID, false)
		require.NoError(t, err)
		_, err = f.svc.InstallDevice(ctx, companyAUser, d.ID, second.ID, false)
		require.NoError(t, err)
		got, err := f.svc.RemoveDevice(ctx, companyAUser, d.ID)
		require.NoError(t, err)
		assert.Nil(t, got.TruckID)

		require.Equal(t, []string{"eld_device.assign_truck", "eld_device.assign_truck", "eld_device.unassign_truck"}, f.audit.actions())
		assert.Equal(t, model.AuditChange{Old: nil, New: first.ID}, f.audit.entries[0].Changes["truck_id"])
		assert.Equal(t, model.AuditChange{Old: first.ID, New: second.ID}, f.audit.entries[1].Changes["truck_id"], "moved between trucks")
		assert.Equal(t, model.AuditChange{Old: second.ID, New: nil}, f.audit.entries[2].Changes["truck_id"])
		assert.Equal(t, model.AuditELDDevice, f.audit.entries[0].Entity)
		assert.Equal(t, &companyA, f.audit.entries[0].CompanyID)
		assert.Len(t, f.elds.ended, 2, "history kept")
		f.audit.requireAllInTx(t)
	})

	t.Run("same truck again changes nothing", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		tr := f.trucks.add(companyA)
		_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)
		require.NoError(t, err)

		got, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

		require.NoError(t, err)
		assert.Equal(t, tr.ID, *got.TruckID)
		assert.Empty(t, f.elds.ended)
		assert.Len(t, f.audit.entries, 1)
	})

	t.Run("truck has another device: 409 without replace", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		old, d := f.devices.add(companyA), f.devices.add(companyA)
		tr := f.trucks.add(companyA)
		_, err := f.svc.InstallDevice(ctx, companyAUser, old.ID, tr.ID, false)
		require.NoError(t, err)

		_, err = f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

		assert.ErrorIs(t, err, model.ErrTruckHasELD)
	})

	t.Run("replace takes the old device out", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		old, d := f.devices.add(companyA), f.devices.add(companyA)
		tr := f.trucks.add(companyA)
		_, err := f.svc.InstallDevice(ctx, companyAUser, old.ID, tr.ID, false)
		require.NoError(t, err)

		got, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, true)

		require.NoError(t, err)
		assert.Equal(t, tr.ID, *got.TruckID)
		assert.NotContains(t, f.elds.open, old.ID, "old device is out")
		assert.Contains(t, f.elds.open, d.ID)
		assert.Equal(t, []string{"eld_device.assign_truck", "eld_device.unassign_truck", "eld_device.assign_truck"}, f.audit.actions())
		assert.Equal(t, old.ID.String(), f.audit.entries[1].EntityID, "the removal is recorded on the old device")
	})

	t.Run("replace into an empty truck is just an install", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		tr := f.trucks.add(companyA)

		_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, true)

		require.NoError(t, err)
		assert.Equal(t, []string{"eld_device.assign_truck"}, f.audit.actions())
	})

	t.Run("other company's truck looks like a missing one", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		foreign := f.trucks.add(uuid.New())

		_, errForeign := f.svc.InstallDevice(ctx, companyAUser, d.ID, foreign.ID, false)
		_, errMissing := f.svc.InstallDevice(ctx, companyAUser, d.ID, uuid.New(), false)

		requireValidationField(t, errForeign, "truck_id")
		assert.Equal(t, errMissing.Error(), errForeign.Error())
		assert.Empty(t, f.elds.open)
	})

	t.Run("platform admin cannot cross companies either", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		foreign := f.trucks.add(uuid.New())

		_, err := f.svc.InstallDevice(ctx, platformAdmin, d.ID, foreign.ID, false)

		requireValidationField(t, err, "truck_id")
	})

	t.Run("out-of-service truck", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		tr := f.trucks.add(companyA)
		tr.Status = model.TruckOutOfService

		_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

		requireValidationField(t, err, "truck_id")
	})

	t.Run("a truck in the shop gets its device there", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(companyA)
		tr := f.trucks.add(companyA)
		tr.Status = model.TruckInMaintenance

		got, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

		require.NoError(t, err)
		assert.Equal(t, tr.ID, *got.TruckID)
	})

	for _, st := range []model.ELDStatus{model.ELDFaulty, model.ELDRetired} {
		t.Run("a "+string(st)+" device cannot be installed", func(t *testing.T) {
			f := newAssignmentSvcFixture()
			d := f.devices.add(companyA)
			d.Status = st
			tr := f.trucks.add(companyA)

			_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

			requireValidationField(t, err, "status")
		})
	}

	t.Run("device of another company does not exist", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.devices.add(uuid.New())
		tr := f.trucks.add(companyA)

		_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)

		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestAssignmentService_RemoveDevice_NotInstalled(t *testing.T) {
	f := newAssignmentSvcFixture()
	d := f.devices.add(companyA)

	got, err := f.svc.RemoveDevice(context.Background(), companyAUser, d.ID)

	require.NoError(t, err)
	assert.Nil(t, got.TruckID)
	assert.Empty(t, f.audit.entries, "nothing changed, nothing recorded")
}

func TestAssignmentService_ReleaseTruck_TakesTheDeviceOutToo(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	d := f.devices.add(companyA)
	tr := f.trucks.add(companyA)
	_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)
	require.NoError(t, err)

	require.NoError(t, f.svc.ReleaseTruck(ctx, companyAUser, tr.ID))

	assert.Empty(t, f.elds.open)
	e := f.audit.last(t)
	assert.Equal(t, "eld_device.unassign_truck", e.Action)
	assert.Equal(t, d.ID.String(), e.EntityID)
}

func TestAssignmentService_ReleaseDevice(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	d := f.devices.add(companyA)
	tr := f.trucks.add(companyA)
	_, err := f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)
	require.NoError(t, err)

	require.NoError(t, f.svc.ReleaseDevice(ctx, companyAUser, d.ID))
	assert.Empty(t, f.elds.open)

	require.NoError(t, f.svc.ReleaseDevice(ctx, companyAUser, d.ID), "not installed: nothing to do")
}

// ---------- reading the board ----------

func TestAssignmentService_TruckBoard(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	tr := f.trucks.add(companyA)
	ali, bob := f.driver(companyA), f.driver(companyA)
	_, err := f.svc.AssignDriver(ctx, companyAUser, bob.Driver.ID, AssignDriverInput{TruckID: tr.ID, Role: model.AssignmentCoDriver})
	require.NoError(t, err)
	_, err = f.svc.AssignDriver(ctx, companyAUser, ali.Driver.ID, AssignDriverInput{TruckID: tr.ID})
	require.NoError(t, err)
	d := f.devices.add(companyA)
	_, err = f.svc.InstallDevice(ctx, companyAUser, d.ID, tr.ID, false)
	require.NoError(t, err)

	b, err := f.svc.TruckBoard(ctx, companyAUser, tr.ID)

	require.NoError(t, err)
	assert.Equal(t, tr.ID, b.Truck.ID)
	require.Len(t, b.Crew, 2)
	assert.Equal(t, ali.Driver.ID, b.Crew[0].Assignment.DriverID, "primary first")
	assert.Equal(t, f.assignments.names[ali.Driver.ID], b.Crew[0].FirstName)
	require.NotNil(t, b.Device)
	assert.Equal(t, d.ID, b.Device.Assignment.ELDDeviceID)
	assert.Zero(t, f.assignments.readOutside, "all reads in one snapshot")
}

func TestAssignmentService_TruckBoard_Empty(t *testing.T) {
	f := newAssignmentSvcFixture()
	tr := f.trucks.add(companyA)

	b, err := f.svc.TruckBoard(context.Background(), companyAUser, tr.ID)

	require.NoError(t, err)
	assert.Empty(t, b.Crew)
	assert.Nil(t, b.Device)
}

func TestAssignmentService_TruckBoard_Scope(t *testing.T) {
	f := newAssignmentSvcFixture()
	foreign := f.trucks.add(uuid.New())

	_, err := f.svc.TruckBoard(context.Background(), companyAUser, foreign.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)

	_, err = f.svc.TruckBoard(context.Background(), platformAdmin, foreign.ID)
	assert.NoError(t, err, "platform users see every truck")
}

func TestAssignmentService_DriverTruck(t *testing.T) {
	ctx := context.Background()

	t.Run("on a truck: stint and the truck's board", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		tr := f.trucks.add(companyA)
		ali, bob := f.driver(companyA), f.driver(companyA)
		_, err := f.svc.AssignDriver(ctx, companyAUser, ali.Driver.ID, AssignDriverInput{TruckID: tr.ID})
		require.NoError(t, err)
		_, err = f.svc.AssignDriver(ctx, companyAUser, bob.Driver.ID, AssignDriverInput{TruckID: tr.ID, Role: model.AssignmentCoDriver})
		require.NoError(t, err)

		dt, err := f.svc.DriverTruck(ctx, companyAUser, bob.Driver.ID)

		require.NoError(t, err)
		assert.Equal(t, bob.Driver.ID, dt.DriverID)
		require.NotNil(t, dt.Assignment)
		assert.Equal(t, model.AssignmentCoDriver, dt.Assignment.Role)
		require.NotNil(t, dt.Board)
		assert.Len(t, dt.Board.Crew, 2, "Bob sees his co-driver")
	})

	t.Run("on no truck", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)

		dt, err := f.svc.DriverTruck(ctx, companyAUser, d.Driver.ID)

		require.NoError(t, err)
		assert.Nil(t, dt.Assignment)
		assert.Nil(t, dt.Board)
	})

	t.Run("driver of another company does not exist", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(uuid.New())

		_, err := f.svc.DriverTruck(ctx, companyAUser, d.Driver.ID)

		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestAssignmentService_MyTruck(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	d := f.driver(companyA)
	tr := f.trucks.add(companyA)
	me := auth.Principal{UserID: d.Driver.UserID, CompanyID: &companyA}
	_, err := f.svc.AssignMe(ctx, me, AssignDriverInput{TruckID: tr.ID})
	require.NoError(t, err)

	dt, err := f.svc.MyTruck(ctx, me)

	require.NoError(t, err)
	require.NotNil(t, dt.Board)
	assert.Equal(t, tr.ID, dt.Board.Truck.ID)

	_, err = f.svc.MyTruck(ctx, companyAUser)
	assert.ErrorIs(t, err, model.ErrNotFound, "not a driver")
}

// ---------- history ----------

func TestAssignmentService_DriverHistory(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 3, 14, 30, 0, 0, time.UTC)

	t.Run("company users are kept in their company", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		other, truckID := uuid.New(), uuid.New()

		page, err := f.svc.DriverHistory(ctx, companyAUser, AssignmentHistoryParams{
			CompanyID: &other, TruckID: &truckID, Period: model.StintPeriod{At: &at}, Page: 2, PageSize: 10})

		require.NoError(t, err)
		assert.Equal(t, &companyA, f.assignments.historyF.CompanyID, "asked for another company: ignored")
		assert.Equal(t, &truckID, f.assignments.historyF.TruckID)
		assert.Equal(t, &at, f.assignments.historyF.Period.At)
		assert.Equal(t, [2]int{10, 10}, f.assignments.historyPage)
		assert.Equal(t, 2, page.Page)
	})

	t.Run("platform users choose the company, or see all", func(t *testing.T) {
		f := newAssignmentSvcFixture()

		_, err := f.svc.DriverHistory(ctx, platformAdmin, AssignmentHistoryParams{})

		require.NoError(t, err)
		assert.Nil(t, f.assignments.historyF.CompanyID)
	})

	t.Run("at and from/to are not combined", func(t *testing.T) {
		f := newAssignmentSvcFixture()

		_, err := f.svc.DriverHistory(ctx, companyAUser, AssignmentHistoryParams{Period: model.StintPeriod{At: &at, From: &at}})

		requireValidationField(t, err, "at")
	})

	t.Run("to must be after from", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		later := at.Add(time.Hour)

		_, err := f.svc.DriverHistory(ctx, companyAUser, AssignmentHistoryParams{Period: model.StintPeriod{From: &later, To: &at}})
		requireValidationField(t, err, "to")

		_, err = f.svc.DriverHistory(ctx, companyAUser, AssignmentHistoryParams{Period: model.StintPeriod{From: &at, To: &at}})
		requireValidationField(t, err, "to")
	})
}

func TestAssignmentService_ELDHistory(t *testing.T) {
	f := newAssignmentSvcFixture()
	deviceID := uuid.New()

	_, err := f.svc.ELDHistory(context.Background(), companyAUser, AssignmentHistoryParams{ELDDeviceID: &deviceID})

	require.NoError(t, err)
	assert.Equal(t, &companyA, f.elds.historyF.CompanyID)
	assert.Equal(t, &deviceID, f.elds.historyF.ELDDeviceID)
}

// ---------- dispatchers ----------

func TestAssignmentService_SetDispatcher(t *testing.T) {
	ctx := context.Background()

	t.Run("links, locking the dispatcher BEFORE the driver", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		disp := f.dispatcher(companyA)

		got, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &disp.ID)

		require.NoError(t, err)
		assert.Equal(t, disp.ID, *got.Driver.DispatcherID)
		assert.Equal(t, disp.ID, *f.drivers.drivers[d.Driver.ID].Driver.DispatcherID, "saved")
		assert.Equal(t, lockLog{"user", "driver"}, f.locks,
			"the same order as deactivating a dispatcher (user, then drivers): no deadlock")
		e := f.audit.last(t)
		assert.Equal(t, "driver.set_dispatcher", e.Action)
		assert.Equal(t, model.AuditChange{Old: nil, New: disp.ID}, e.Changes["dispatcher_id"])
		f.audit.requireAllInTx(t)
	})

	t.Run("the same again changes nothing", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		disp := f.dispatcher(companyA)
		_, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &disp.ID)
		require.NoError(t, err)

		_, err = f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &disp.ID)

		require.NoError(t, err)
		assert.Len(t, f.audit.entries, 1)
	})

	t.Run("unlink", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		disp := f.dispatcher(companyA)
		_, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &disp.ID)
		require.NoError(t, err)

		got, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, nil)

		require.NoError(t, err)
		assert.Nil(t, got.Driver.DispatcherID)
		assert.Equal(t, model.AuditChange{Old: disp.ID, New: nil}, f.audit.last(t).Changes["dispatcher_id"])

		_, err = f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, nil)
		require.NoError(t, err, "no dispatcher: unlinking is not an error")
		assert.Len(t, f.audit.entries, 2, "and nothing recorded")
	})

	invalid := []struct {
		name  string
		setup func(f *assignmentSvcFixture, d *model.DriverDetails) uuid.UUID
		msg   string
	}{
		{"unknown user", func(*assignmentSvcFixture, *model.DriverDetails) uuid.UUID { return uuid.New() }, "user not found"},
		{"another company's user looks like a missing one", func(f *assignmentSvcFixture, _ *model.DriverDetails) uuid.UUID {
			return f.dispatcher(uuid.New()).ID
		}, "user not found"},
		{"no permission to dispatch", func(f *assignmentSvcFixture, _ *model.DriverDetails) uuid.UUID {
			u := f.dispatcher(companyA)
			f.users.perms[u.ID] = []model.Permission{model.PermDriverRead}
			return u.ID
		}, "this user cannot dispatch (needs permission assignment:manage)"},
		{"disabled account", func(f *assignmentSvcFixture, _ *model.DriverDetails) uuid.UUID {
			u := f.dispatcher(companyA)
			u.IsActive = false
			return u.ID
		}, "the user's account is disabled"},
		{"the driver themselves", func(f *assignmentSvcFixture, d *model.DriverDetails) uuid.UUID {
			cid := companyA
			f.users.users[d.Driver.UserID] = &model.User{ID: d.Driver.UserID, CompanyID: &cid, IsActive: true}
			f.users.perms[d.Driver.UserID] = []model.Permission{model.PermAssignmentManage}
			return d.Driver.UserID
		}, "a driver cannot be their own dispatcher"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			f := newAssignmentSvcFixture()
			d := f.driver(companyA)
			id := tc.setup(f, d)

			_, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &id)

			var vErr *validation.ValidationError
			require.ErrorAs(t, err, &vErr)
			assert.Equal(t, tc.msg, vErr.Fields["dispatcher_id"])
			assert.Nil(t, f.drivers.drivers[d.Driver.ID].Driver.DispatcherID, "not saved")
		})
	}

	t.Run("driver of another company does not exist, before any lock", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(uuid.New())
		disp := f.dispatcher(companyA)

		_, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &disp.ID)

		assert.ErrorIs(t, err, model.ErrNotFound)
		assert.Empty(t, f.locks)
	})
}

func TestAssignmentService_ReleaseUser_UnlinksADispatchersDrivers(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	disp := f.dispatcher(companyA)
	ali, bob := f.driver(companyA), f.driver(companyA)
	for _, d := range []*model.DriverDetails{ali, bob} {
		_, err := f.svc.SetDispatcher(ctx, companyAUser, d.Driver.ID, &disp.ID)
		require.NoError(t, err)
	}

	require.NoError(t, f.svc.ReleaseUser(ctx, companyAUser, disp.ID))

	assert.Nil(t, f.drivers.drivers[ali.Driver.ID].Driver.DispatcherID)
	assert.Nil(t, f.drivers.drivers[bob.Driver.ID].Driver.DispatcherID)
	assert.Equal(t, []string{"driver.set_dispatcher", "driver.set_dispatcher", "driver.set_dispatcher", "driver.set_dispatcher"},
		f.audit.actions(), "two links, two unlinks")
}

func TestAssignmentService_ReleaseDrivers_KeepsTheDevice(t *testing.T) {
	ctx := context.Background()
	f := newAssignmentSvcFixture()
	tr := f.trucks.add(companyA)
	d := f.driver(companyA)
	_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})
	require.NoError(t, err)
	dev := f.devices.add(companyA)
	_, err = f.svc.InstallDevice(ctx, companyAUser, dev.ID, tr.ID, false)
	require.NoError(t, err)

	require.NoError(t, f.svc.ReleaseDrivers(ctx, companyAUser, tr.ID))

	assert.Empty(t, f.assignments.open, "drivers off")
	assert.Contains(t, f.elds.open, dev.ID, "the device stays in the truck")
}
