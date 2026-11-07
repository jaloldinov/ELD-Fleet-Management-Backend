package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type assignmentFixture struct {
	*driverFixture
	assignments *DriverAssignmentRepository
	trucks      *TruckRepository
	t1, t2      *model.Truck // alpha's trucks
	ali, bob    uuid.UUID    // alpha's drivers
	dan         uuid.UUID    // bravo's driver
}

func newAssignmentFixture(t *testing.T) *assignmentFixture {
	t.Helper()
	f := &assignmentFixture{driverFixture: newDriverFixture(t)}
	f.assignments = NewDriverAssignmentRepository(f.db)
	f.trucks = NewTruckRepository(f.db)
	ctx := context.Background()
	var err error
	f.t1, err = f.trucks.Create(ctx, newTruck(f.alpha.ID, "101", vin1))
	require.NoError(t, err)
	f.t2, err = f.trucks.Create(ctx, newTruck(f.alpha.ID, "102", vin2))
	require.NoError(t, err)
	f.ali = f.driver(t, f.alpha.ID)
	f.bob = f.driver(t, f.alpha.ID)
	f.dan = f.driver(t, f.bravo.ID)
	return f
}

func (f *assignmentFixture) driver(t *testing.T, companyID uuid.UUID) uuid.UUID {
	t.Helper()
	id, err := f.createDriver(context.Background(), companyID, "TX", fmt.Sprintf("L%07d", f.n+1))
	require.NoError(t, err)
	return id
}

func (f *assignmentFixture) open(driverID uuid.UUID, truck *model.Truck, role model.AssignmentRole) (*model.DriverAssignment, error) {
	return f.assignments.Open(context.Background(), model.DriverAssignment{
		CompanyID: truck.CompanyID, DriverID: driverID, TruckID: truck.ID, Role: role,
	})
}

func TestDriverAssignmentRepository_OpenAndCurrent(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()

	// Co-driver first, to prove the list puts the primary first anyway.
	_, err := f.open(f.bob, f.t1, model.AssignmentCoDriver)
	require.NoError(t, err)
	opened, err := f.open(f.ali, f.t1, model.AssignmentPrimary)
	require.NoError(t, err)
	assert.True(t, opened.IsCurrent())
	assert.Nil(t, opened.StartedBy, "nil = opened by the system")

	got, err := f.assignments.GetOpenByDriver(ctx, f.ali)
	require.NoError(t, err)
	assert.Equal(t, opened.ID, got.ID)
	assert.Equal(t, model.AssignmentPrimary, got.Role)

	inTruck, err := f.assignments.ListOpenByTruck(ctx, f.t1.ID)
	require.NoError(t, err)
	require.Len(t, inTruck, 2)
	assert.Equal(t, f.ali, inTruck[0].DriverID, "primary first")
	assert.Equal(t, f.bob, inTruck[1].DriverID)

	empty, err := f.assignments.ListOpenByTruck(ctx, f.t2.ID)
	require.NoError(t, err)
	assert.Empty(t, empty)

	_, err = f.assignments.GetOpenByDriver(ctx, f.dan)
	assert.ErrorIs(t, err, model.ErrNotFound, "dan is on no truck")
}

func TestDriverAssignmentRepository_Rules(t *testing.T) {
	f := newAssignmentFixture(t)
	_, err := f.open(f.ali, f.t1, model.AssignmentPrimary)
	require.NoError(t, err)

	t.Run("one primary per truck", func(t *testing.T) {
		_, err := f.open(f.bob, f.t1, model.AssignmentPrimary)
		assert.ErrorIs(t, err, model.ErrSeatTaken)
	})

	t.Run("one truck per driver", func(t *testing.T) {
		_, err := f.open(f.ali, f.t2, model.AssignmentPrimary)
		assert.ErrorIs(t, err, model.ErrDriverOnTruck)
	})

	t.Run("driver of another company is refused by the database", func(t *testing.T) {
		_, err := f.open(f.dan, f.t2, model.AssignmentPrimary)
		assert.ErrorContains(t, err, "dta_driver_company_fkey")
	})

	t.Run("unknown role is refused by the database", func(t *testing.T) {
		_, err := f.open(f.bob, f.t2, "passenger")
		assert.ErrorContains(t, err, "dta_role")
	})
}

func TestDriverAssignmentRepository_EndAndMove(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	first, err := f.open(f.ali, f.t1, model.AssignmentPrimary)
	require.NoError(t, err)
	ali, err := f.drivers.GetByID(ctx, f.ali)
	require.NoError(t, err)
	by := &ali.User.ID // Ali ends the stint himself (self-service)

	ended, err := f.assignments.EndOpenByDriver(ctx, f.ali, by)
	require.NoError(t, err)
	assert.Equal(t, first.ID, ended.ID)
	assert.False(t, ended.IsCurrent())
	assert.Equal(t, *by, *ended.EndedBy)
	assert.False(t, ended.EndedAt.Before(ended.StartedAt))

	_, err = f.assignments.GetOpenByDriver(ctx, f.ali)
	assert.ErrorIs(t, err, model.ErrNotFound)
	_, err = f.assignments.EndOpenByDriver(ctx, f.ali, nil)
	assert.ErrorIs(t, err, model.ErrNotFound, "nothing left to end")

	// Moving = end + open. Ali is free for truck 102, and 101's primary
	// seat is free for Bob.
	_, err = f.open(f.ali, f.t2, model.AssignmentPrimary)
	assert.NoError(t, err)
	_, err = f.open(f.bob, f.t1, model.AssignmentPrimary)
	assert.NoError(t, err)
}

func TestDriverAssignmentRepository_EndOpenByTruck(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	_, err := f.open(f.ali, f.t1, model.AssignmentPrimary)
	require.NoError(t, err)
	_, err = f.open(f.bob, f.t1, model.AssignmentCoDriver)
	require.NoError(t, err)

	ended, err := f.assignments.EndOpenByTruck(ctx, f.t1.ID, nil)
	require.NoError(t, err)
	assert.Len(t, ended, 2)
	for _, a := range ended {
		assert.False(t, a.IsCurrent())
		assert.Nil(t, a.EndedBy, "ended by the system")
	}

	again, err := f.assignments.EndOpenByTruck(ctx, f.t1.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, again, "none open is not an error")
}

// 20 dispatchers put 20 different drivers into the same primary seat at the
// same moment. The partial unique index lets exactly one win.
func TestDriverAssignmentRepository_ConcurrentSeatOnlyOneWins(t *testing.T) {
	f := newAssignmentFixture(t)
	const attempts = 20
	drivers := make([]uuid.UUID, attempts)
	for i := range drivers {
		drivers[i] = f.driver(t, f.alpha.ID)
	}
	warmPool(t, f, attempts)

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		errs  = make([]error, attempts)
	)
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = f.open(drivers[i], f.t1, model.AssignmentPrimary)
		}()
	}
	close(start)
	wg.Wait()

	successes, taken := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, model.ErrSeatTaken):
			taken++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, attempts-1, taken)
}

// warmPool opens n connections in advance, so goroutines that start together
// really overlap instead of each spending milliseconds connecting first.
func warmPool(t *testing.T, f *assignmentFixture, n int) {
	t.Helper()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.db.Exec(context.Background(), `SELECT pg_sleep(0.05)`)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}

func TestLockByID_RequiresTransaction(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()

	_, err := f.trucks.LockByID(ctx, f.t1.ID)
	assert.ErrorIs(t, err, errNoTx)
	_, err = f.drivers.LockByID(ctx, f.ali)
	assert.ErrorIs(t, err, errNoTx)

	err = f.tx.WithinTx(ctx, func(ctx context.Context) error {
		truck, err := f.trucks.LockByID(ctx, f.t1.ID)
		require.NoError(t, err)
		assert.Equal(t, "101", truck.UnitNumber)
		d, err := f.drivers.LockByID(ctx, f.ali)
		require.NoError(t, err)
		assert.Equal(t, f.ali, d.Driver.ID)
		return nil
	})
	require.NoError(t, err)
}

// The race this lock fixes: request A locks truck 101 and is deleting it.
// Request B (an assignment) wants the same truck. B must WAIT for A, and
// then see the truck as A left it (deleted), not as it was before.
func TestTruckRepository_LockByID_WaitsAndSeesNewState(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()

	locked := make(chan struct{})  // A holds the lock
	release := make(chan struct{}) // test lets A finish
	// Even if the test fails early, A must finish: a transaction left open
	// would make closing the pool (test cleanup) wait forever.
	releaseA := sync.OnceFunc(func() { close(release) })
	defer releaseA()
	aDone := make(chan error, 1)
	go func() {
		aDone <- f.tx.WithinTx(ctx, func(ctx context.Context) error {
			if _, err := f.trucks.LockByID(ctx, f.t1.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return f.trucks.SoftDelete(ctx, f.t1.ID)
		})
	}()
	<-locked

	bDone := make(chan error, 1)
	go func() {
		bDone <- f.tx.WithinTx(ctx, func(ctx context.Context) error {
			_, err := f.trucks.LockByID(ctx, f.t1.ID)
			return err
		})
	}()

	select {
	case err := <-bDone:
		t.Fatalf("B did not wait for A's lock (err: %v)", err)
	case <-time.After(200 * time.Millisecond):
		// B is blocked on the lock, as it should be.
	}

	releaseA()
	require.NoError(t, <-aDone)
	assert.ErrorIs(t, <-bDone, model.ErrNotFound, "B re-reads the row after A's COMMIT")
}

func TestDriverAssignmentRepository_EndOpenByDriverUser(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	_, err := f.open(f.ali, f.t1, model.AssignmentPrimary)
	require.NoError(t, err)
	ali, err := f.drivers.GetByID(ctx, f.ali)
	require.NoError(t, err)

	// DriverService deletes the profile first, then the user, in one
	// transaction: the stint must still be found through the deleted profile.
	require.NoError(t, f.drivers.SoftDelete(ctx, f.ali))
	ended, err := f.assignments.EndOpenByDriverUser(ctx, ali.User.ID, nil)

	require.NoError(t, err)
	require.Len(t, ended, 1)
	assert.Equal(t, f.t1.ID, ended[0].TruckID)
	assert.False(t, ended[0].IsCurrent())

	none, err := f.assignments.EndOpenByDriverUser(ctx, uuid.New(), nil)
	require.NoError(t, err)
	assert.Empty(t, none, "not a driver: nothing to end")
}

// The race FOR SHARE OF u closes: an assignment reads "account active" while
// an admin disables the account. Whoever comes second must wait, and then
// see what the first one did.
func TestDriverRepository_LockByID_HoldsTheUserAccount(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	ali, err := f.drivers.GetByID(ctx, f.ali)
	require.NoError(t, err)
	// conn: inside a transaction when ctx carries one, like repository code.
	disable := func(ctx context.Context) error {
		_, err := conn(ctx, f.db).Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, ali.User.ID)
		return err
	}

	t.Run("assignment first: the deactivation waits", func(t *testing.T) {
		locked, release := make(chan struct{}), make(chan struct{})
		releaseA := sync.OnceFunc(func() { close(release) })
		defer releaseA()
		aDone := make(chan error, 1)
		go func() {
			aDone <- f.tx.WithinTx(ctx, func(ctx context.Context) error {
				if _, err := f.drivers.LockByID(ctx, f.ali); err != nil {
					return err
				}
				close(locked)
				<-release
				return nil
			})
		}()
		<-locked

		bDone := make(chan error, 1)
		go func() { bDone <- disable(ctx) }()
		select {
		case err := <-bDone:
			t.Fatalf("the deactivation did not wait for the assignment (err: %v)", err)
		case <-time.After(200 * time.Millisecond):
		}
		releaseA()
		require.NoError(t, <-aDone)
		require.NoError(t, <-bDone)
	})

	_, err = f.db.Exec(ctx, `UPDATE users SET is_active = true WHERE id = $1`, ali.User.ID)
	require.NoError(t, err)

	t.Run("deactivation first: the assignment waits and sees it", func(t *testing.T) {
		locked, release := make(chan struct{}), make(chan struct{})
		releaseB := sync.OnceFunc(func() { close(release) })
		defer releaseB()
		bDone := make(chan error, 1)
		go func() {
			bDone <- f.tx.WithinTx(ctx, func(ctx context.Context) error {
				if err := disable(ctx); err != nil {
					return err
				}
				close(locked)
				<-release
				return nil
			})
		}()
		<-locked

		type result struct {
			active bool
			err    error
		}
		aDone := make(chan result, 1)
		go func() {
			var r result
			r.err = f.tx.WithinTx(ctx, func(ctx context.Context) error {
				dd, err := f.drivers.LockByID(ctx, f.ali)
				if err == nil {
					r.active = dd.User.IsActive
				}
				return err
			})
			aDone <- r
		}()
		select {
		case r := <-aDone:
			t.Fatalf("the assignment did not wait for the deactivation (%+v)", r)
		case <-time.After(200 * time.Millisecond):
		}
		releaseB()
		require.NoError(t, <-bDone)
		r := <-aDone
		require.NoError(t, r.err)
		assert.False(t, r.active, "read after the deactivation's COMMIT, not before")
	})
}
