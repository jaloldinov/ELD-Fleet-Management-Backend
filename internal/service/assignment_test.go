package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// memAssignments is an in-memory stint store with the same two rules as the
// database's partial unique indexes.
type memAssignments struct {
	open  map[uuid.UUID]*model.DriverAssignment // by driver
	ended []model.DriverAssignment
}

func newMemAssignments() *memAssignments {
	return &memAssignments{open: map[uuid.UUID]*model.DriverAssignment{}}
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

type assignmentSvcFixture struct {
	svc         *AssignmentService
	assignments *memAssignments
	drivers     *memDriverRepo
	trucks      *memTruckRepo
	audit       *memAuditStore
	locks       lockLog
}

func newAssignmentSvcFixture() *assignmentSvcFixture {
	f := &assignmentSvcFixture{assignments: newMemAssignments(), drivers: newMemDriverRepo(), trucks: newMemTruckRepo()}
	var audit *Auditor
	audit, f.audit = newTestAuditor()
	f.svc = NewAssignmentService(f.assignments,
		lockingDrivers{f.drivers, &f.locks}, lockingTrucks{f.trucks, &f.locks}, audit)
	return f
}

// driver adds an active driver of the company.
func (f *assignmentSvcFixture) driver(companyID uuid.UUID) *model.DriverDetails {
	dd := f.drivers.add(companyID)
	dd.User.IsActive = true
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

	t.Run("out-of-service truck", func(t *testing.T) {
		f := newAssignmentSvcFixture()
		d := f.driver(companyA)
		tr := f.trucks.add(companyA)
		tr.IsActive = false

		_, err := f.svc.AssignDriver(ctx, companyAUser, d.Driver.ID, AssignDriverInput{TruckID: tr.ID})

		requireValidationField(t, err, "truck_id")
	})

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
