package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// memDutyEvents is an in-memory logbook with the database's rules: one
// ACTIVE record per driver per instant, a version replaced once; reads see
// active records only.
type memDutyEvents struct {
	rows    []model.DutyStatusEvent
	certs   []model.LogCertification
	drivers *memDutyDrivers
	now     func() time.Time // the database's now(): created_at, inactivated_at
}

func active(r model.DutyStatusEvent) bool { return r.RecordStatus != model.RecordInactive }

func (m *memDutyEvents) Insert(ctx context.Context, e model.DutyStatusEvent) (*model.DutyStatusEvent, error) {
	if !inTx(ctx) {
		return nil, errors.New("insert outside a transaction")
	}
	for _, r := range m.rows {
		if active(r) && r.DriverID == e.DriverID && r.StartedAt.Equal(e.StartedAt) {
			return nil, model.ErrDutyStatusTaken
		}
		if e.Replaces != nil && r.Replaces != nil && *r.Replaces == *e.Replaces {
			return nil, model.ErrRecordReplaced
		}
	}
	e.ID, e.CreatedAt, e.RecordStatus = uuid.New(), e.StartedAt, model.RecordActive
	if m.now != nil {
		e.CreatedAt = m.now()
	}
	m.rows = append(m.rows, e)
	sort.Slice(m.rows, func(i, j int) bool { return m.rows[i].StartedAt.Before(m.rows[j].StartedAt) })
	return &e, nil
}

func (m *memDutyEvents) At(ctx context.Context, driverID uuid.UUID, at time.Time) (*model.DutyStatusEvent, error) {
	if inTx(ctx) && !m.drivers.locked[driverID] {
		return nil, errors.New("read the current status before locking the driver")
	}
	var found *model.DutyStatusEvent
	for i := range m.rows {
		if r := m.rows[i]; active(r) && r.DriverID == driverID && !r.StartedAt.After(at) {
			found = &r
		}
	}
	if found == nil {
		return nil, model.ErrNotFound
	}
	return found, nil
}

func (m *memDutyEvents) ListPeriod(_ context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error) {
	var out []model.DutyStatusEvent
	for _, r := range m.rows { // sorted by StartedAt
		if !active(r) || r.DriverID != driverID || !r.StartedAt.Before(to) {
			continue
		}
		if !r.StartedAt.After(from) && len(out) > 0 {
			out = out[1:] // only the LAST one at or before from is in force
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *memDutyEvents) ListReplaced(_ context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error) {
	var out []model.DutyStatusEvent
	for _, r := range m.rows {
		if !active(r) && r.DriverID == driverID && !r.StartedAt.Before(from) && r.StartedAt.Before(to) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memDutyEvents) GetByID(_ context.Context, id uuid.UUID) (*model.DutyStatusEvent, error) {
	for _, r := range m.rows {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, model.ErrNotFound
}

func (m *memDutyEvents) Inactivate(ctx context.Context, id uuid.UUID, by uuid.UUID) error {
	if !inTx(ctx) {
		return errors.New("inactivate outside a transaction")
	}
	for i, r := range m.rows {
		if r.ID == id && active(r) {
			at := m.now()
			m.rows[i].RecordStatus, m.rows[i].InactivatedAt, m.rows[i].InactivatedBy = model.RecordInactive, &at, &by
			return nil
		}
	}
	return model.ErrRecordReplaced
}

func (m *memDutyEvents) Certify(_ context.Context, c model.LogCertification) (*model.LogCertification, error) {
	c.ID, c.CertifiedAt = uuid.New(), m.now()
	m.certs = append(m.certs, c)
	return &c, nil
}

func (m *memDutyEvents) LastCertification(_ context.Context, driverID uuid.UUID, date string) (*time.Time, error) {
	var last *time.Time
	for _, c := range m.certs {
		if c.DriverID == driverID && c.Date == date && (last == nil || c.CertifiedAt.After(*last)) {
			at := c.CertifiedAt
			last = &at
		}
	}
	return last, nil
}

type memDutyDrivers struct {
	byUser map[uuid.UUID]model.Driver
	locked map[uuid.UUID]bool
}

func (m *memDutyDrivers) GetByUserID(_ context.Context, userID uuid.UUID) (*model.DriverDetails, error) {
	d, ok := m.byUser[userID]
	if !ok {
		return nil, model.ErrNotFound
	}
	return &model.DriverDetails{Driver: d}, nil
}

func (m *memDutyDrivers) GetByID(_ context.Context, id uuid.UUID) (*model.DriverDetails, error) {
	for _, d := range m.byUser {
		if d.ID == id {
			return &model.DriverDetails{Driver: d}, nil
		}
	}
	return nil, model.ErrNotFound
}

type memDutyCompanies map[uuid.UUID]string // id → time zone

func (m memDutyCompanies) GetByID(_ context.Context, id uuid.UUID) (*model.Company, error) {
	tz, ok := m[id]
	if !ok {
		return nil, model.ErrNotFound
	}
	return &model.Company{ID: id, Timezone: tz}, nil
}

func (m *memDutyDrivers) LockByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error) {
	if !inTx(ctx) {
		return nil, errors.New("lock outside a transaction")
	}
	m.locked[id] = true
	return &model.DriverDetails{}, nil
}

// memDutyStints: who is on which truck. Time is not modelled: a stint
// covers every instant.
type memDutyStints map[uuid.UUID]model.DriverAssignment // by driver

func (m memDutyStints) GetOpenByDriver(_ context.Context, driverID uuid.UUID) (*model.DriverAssignment, error) {
	a, ok := m[driverID]
	if !ok {
		return nil, model.ErrNotFound
	}
	return &a, nil
}

func (m memDutyStints) PrimaryAt(_ context.Context, truckID uuid.UUID, _ time.Time) (*model.DriverAssignment, error) {
	for _, a := range m {
		if a.TruckID == truckID && a.Role == model.AssignmentPrimary {
			return &a, nil
		}
	}
	return nil, model.ErrNotFound
}

type memDutyDevices struct {
	installed map[uuid.UUID]*model.InstalledDevice // by truck
	truckOf   map[uuid.UUID]uuid.UUID              // device → truck
}

func (m memDutyDevices) GetInstalled(_ context.Context, truckID uuid.UUID) (*model.InstalledDevice, error) {
	d, ok := m.installed[truckID]
	if !ok {
		return nil, model.ErrNotFound
	}
	return d, nil
}

func (m memDutyDevices) TruckAt(_ context.Context, deviceID uuid.UUID, _ time.Time) (uuid.UUID, error) {
	t, ok := m.truckOf[deviceID]
	if !ok {
		return uuid.Nil, model.ErrNotFound
	}
	return t, nil
}

type dutyFixture struct {
	svc     *DutyStatusService
	events  *memDutyEvents
	stints  memDutyStints
	devices memDutyDevices
	clock   time.Time
	me      auth.Principal
	driver  model.Driver
	truck   uuid.UUID
}

func newDutyFixture() *dutyFixture {
	company := uuid.New()
	f := &dutyFixture{
		clock:  time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC),
		driver: model.Driver{ID: uuid.New(), UserID: uuid.New(), CompanyID: company},
		truck:  uuid.New(), stints: memDutyStints{},
		devices: memDutyDevices{installed: map[uuid.UUID]*model.InstalledDevice{}, truckOf: map[uuid.UUID]uuid.UUID{}},
	}
	f.me = auth.Principal{UserID: f.driver.UserID, CompanyID: &company}
	drivers := &memDutyDrivers{byUser: map[uuid.UUID]model.Driver{f.driver.UserID: f.driver}, locked: map[uuid.UUID]bool{}}
	f.events = &memDutyEvents{drivers: drivers}
	f.svc = NewDutyStatusService(&fakeTx{}, f.events, drivers, f.stints, f.devices,
		memDutyCompanies{company: "America/Chicago"})
	f.svc.now = func() time.Time { return f.clock }
	f.events.now = f.svc.now
	return f
}

// board puts the driver on the truck in a role.
func (f *dutyFixture) board(role model.AssignmentRole) {
	f.stints[f.driver.ID] = model.DriverAssignment{CompanyID: f.driver.CompanyID, DriverID: f.driver.ID, TruckID: f.truck, Role: role}
}

func (f *dutyFixture) change(status model.DutyStatus) (*model.DutyStatusEvent, bool, error) {
	return f.svc.ChangeMyStatus(context.Background(), f.me, ChangeDutyStatusInput{Status: status})
}

func TestDutyStatusService_ChangeMyStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("records the change now, by the driver, on no truck", func(t *testing.T) {
		f := newDutyFixture()
		f.clock = f.clock.Add(123 * time.Millisecond)

		e, created, err := f.change(model.DutyOnDuty)

		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, model.DutyOnDuty, e.Status)
		assert.Equal(t, model.DutyOriginDriver, e.Origin)
		assert.Equal(t, f.driver.ID, e.DriverID)
		assert.Equal(t, f.driver.CompanyID, e.CompanyID)
		assert.Equal(t, f.me.UserID, *e.CreatedBy)
		assert.Equal(t, time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC), e.StartedAt, "whole seconds")
		assert.Nil(t, e.TruckID, "on no truck")
		assert.Nil(t, e.OdometerMi)
	})

	t.Run("the same status again changes nothing", func(t *testing.T) {
		f := newDutyFixture()
		first, _, err := f.change(model.DutySleeperBerth)
		require.NoError(t, err)
		f.clock = f.clock.Add(10 * time.Minute)

		again, created, err := f.change(model.DutySleeperBerth)

		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, first.ID, again.ID, "the status in force comes back")
		assert.Len(t, f.events.rows, 1)
	})

	t.Run("adding personal conveyance to off duty is a change", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentPrimary)
		_, _, err := f.change(model.DutyOffDuty)
		require.NoError(t, err)
		f.clock = f.clock.Add(time.Minute)

		pc := model.SpecialPersonalConveyance
		e, created, err := f.svc.ChangeMyStatus(ctx, f.me, ChangeDutyStatusInput{Status: model.DutyOffDuty, Special: &pc})

		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, pc, *e.Special)
	})

	t.Run("two changes in the same second: the second is refused", func(t *testing.T) {
		f := newDutyFixture()
		_, _, err := f.change(model.DutyOnDuty)
		require.NoError(t, err)
		f.clock = f.clock.Add(400 * time.Millisecond)

		_, _, err = f.change(model.DutyOffDuty)

		assert.ErrorIs(t, err, model.ErrDutyStatusTaken)
	})

	t.Run("on a truck: where it happened comes from the truck's ELD", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentPrimary)
		lat, lon := 32.7767, -96.797
		f.devices.installed[f.truck] = &model.InstalledDevice{State: &model.DeviceState{
			TruckID:        &f.truck,
			TelemetryPoint: model.TelemetryPoint{Latitude: &lat, Longitude: &lon, OdometerMi: 120500.5, EngineHours: 8012.3},
		}}

		e, _, err := f.change(model.DutyOnDuty)

		require.NoError(t, err)
		assert.Equal(t, f.truck, *e.TruckID)
		assert.Equal(t, lat, *e.Latitude)
		assert.Equal(t, 120500.5, *e.OdometerMi)
		assert.Equal(t, 8012.3, *e.EngineHours)
	})

	t.Run("a reading the device took in another truck is not used", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentPrimary)
		other := uuid.New()
		f.devices.installed[f.truck] = &model.InstalledDevice{State: &model.DeviceState{
			TruckID: &other, TelemetryPoint: model.TelemetryPoint{OdometerMi: 99},
		}}

		e, _, err := f.change(model.DutyOnDuty)

		require.NoError(t, err)
		assert.Equal(t, f.truck, *e.TruckID, "the truck is still known")
		assert.Nil(t, e.OdometerMi, "another truck's odometer would be a lie")
	})

	t.Run("a truck without an ELD or a reading: truck only", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentPrimary)
		e, _, err := f.change(model.DutyOnDuty)
		require.NoError(t, err)
		assert.Equal(t, f.truck, *e.TruckID)
		assert.Nil(t, e.Latitude)

		f.devices.installed[f.truck] = &model.InstalledDevice{} // installed, never reported
		f.clock = f.clock.Add(time.Minute)
		e, _, err = f.change(model.DutyOffDuty)
		require.NoError(t, err)
		assert.Nil(t, e.OdometerMi)
	})

	t.Run("special driving needs a truck", func(t *testing.T) {
		f := newDutyFixture()
		ym := model.SpecialYardMoves

		_, _, err := f.svc.ChangeMyStatus(ctx, f.me, ChangeDutyStatusInput{Status: model.DutyOnDuty, Special: &ym})

		var vErr *validation.ValidationError
		require.ErrorAs(t, err, &vErr)
		assert.Contains(t, vErr.Fields, "special")
		assert.Empty(t, f.events.rows)
	})

	t.Run("the note is trimmed; blank means none", func(t *testing.T) {
		f := newDutyFixture()
		note := "  fueling at Love's  "
		e, _, err := f.svc.ChangeMyStatus(ctx, f.me, ChangeDutyStatusInput{Status: model.DutyOnDuty, Note: &note})
		require.NoError(t, err)
		assert.Equal(t, "fueling at Love's", *e.Note)

		blank := "   "
		f.clock = f.clock.Add(time.Minute)
		e, _, err = f.svc.ChangeMyStatus(ctx, f.me, ChangeDutyStatusInput{Status: model.DutyOffDuty, Note: &blank})
		require.NoError(t, err)
		assert.Nil(t, e.Note)
	})

	t.Run("refused before touching the database", func(t *testing.T) {
		pc, ym := model.SpecialPersonalConveyance, model.SpecialYardMoves
		long := strings.Repeat("ж", MaxDutyNoteLength+1)
		cases := map[string]struct {
			in    ChangeDutyStatusInput
			field string
		}{
			"driving is never chosen":     {ChangeDutyStatusInput{Status: model.DutyDriving}, "status"},
			"unknown status":              {ChangeDutyStatusInput{Status: "resting"}, "status"},
			"empty status":                {ChangeDutyStatusInput{}, "status"},
			"personal conveyance on duty": {ChangeDutyStatusInput{Status: model.DutyOnDuty, Special: &pc}, "special"},
			"yard moves in the sleeper":   {ChangeDutyStatusInput{Status: model.DutySleeperBerth, Special: &ym}, "special"},
			"note too long":               {ChangeDutyStatusInput{Status: model.DutyOnDuty, Note: &long}, "note"},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				f := newDutyFixture()
				_, _, err := f.svc.ChangeMyStatus(ctx, f.me, c.in)

				var vErr *validation.ValidationError
				require.ErrorAs(t, err, &vErr)
				assert.Contains(t, vErr.Fields, c.field)
				assert.Empty(t, f.events.rows)
			})
		}
	})

	t.Run("a note of exactly 500 letters is fine", func(t *testing.T) {
		f := newDutyFixture()
		note := strings.Repeat("ж", MaxDutyNoteLength) // 1000 bytes, 500 letters
		_, _, err := f.svc.ChangeMyStatus(ctx, f.me, ChangeDutyStatusInput{Status: model.DutyOnDuty, Note: &note})
		assert.NoError(t, err)
	})

	t.Run("not a driver", func(t *testing.T) {
		f := newDutyFixture()
		office := auth.Principal{UserID: uuid.New(), CompanyID: f.me.CompanyID}
		_, _, err := f.svc.ChangeMyStatus(ctx, office, ChangeDutyStatusInput{Status: model.DutyOnDuty})
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestDutyStatusService_MyStatus(t *testing.T) {
	ctx := context.Background()
	f := newDutyFixture()

	got, err := f.svc.MyStatus(ctx, f.me)
	require.NoError(t, err)
	assert.Nil(t, got, "nothing recorded yet is not an error")

	_, _, err = f.change(model.DutyOnDuty)
	require.NoError(t, err)
	f.clock = f.clock.Add(time.Hour)
	_, _, err = f.change(model.DutyOffDuty)
	require.NoError(t, err)

	got, err = f.svc.MyStatus(ctx, f.me)
	require.NoError(t, err)
	assert.Equal(t, model.DutyOffDuty, got.Status, "the latest change is in force")

	_, err = f.svc.MyStatus(ctx, auth.Principal{UserID: uuid.New()})
	assert.ErrorIs(t, err, model.ErrNotFound, "not a driver")
}

func TestDutyStatusService_ChangeMyStatus_TruckMoving(t *testing.T) {
	moving := func(f *dutyFixture, readingAge time.Duration) {
		f.devices.installed[f.truck] = &model.InstalledDevice{State: &model.DeviceState{
			TruckID: &f.truck, Moving: true,
			TelemetryPoint: model.TelemetryPoint{RecordedAt: f.clock.Add(-readingAge), SpeedMPH: 61},
		}}
	}

	t.Run("at the wheel of a moving truck: refused", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentPrimary)
		moving(f, 30*time.Second)

		_, _, err := f.change(model.DutyOffDuty)

		assert.ErrorIs(t, err, model.ErrTruckMoving)
		assert.Empty(t, f.events.rows)
	})

	t.Run("the co-driver may: they are not driving", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentCoDriver)
		moving(f, 30*time.Second)

		_, created, err := f.change(model.DutySleeperBerth)

		require.NoError(t, err)
		assert.True(t, created)
	})

	t.Run("a stale 'moving' says nothing about now", func(t *testing.T) {
		f := newDutyFixture()
		f.board(model.AssignmentPrimary)
		moving(f, model.DeviceOfflineAfter+time.Second) // lost signal in a tunnel, or power

		_, _, err := f.change(model.DutyOnDuty)

		assert.NoError(t, err)
	})
}

func TestDutyStatusService_RecordMotion(t *testing.T) {
	inTxCtx := context.WithValue(context.Background(), inTxKey{}, true) // the ingest's transaction
	device := uuid.New()
	newMotionFixture := func(role model.AssignmentRole) *dutyFixture {
		f := newDutyFixture()
		f.board(role)
		f.devices.truckOf[device] = f.truck
		return f
	}
	lat, lon := 32.7767, -96.797
	reading := model.TelemetryPoint{Latitude: &lat, Longitude: &lon, SpeedMPH: 30, OdometerMi: 120001, EngineHours: 8000.5}
	change := func(kind model.MotionChangeKind, at time.Time) model.MotionChange {
		return model.MotionChange{Kind: kind, At: at, Point: reading}
	}
	statuses := func(f *dutyFixture) (out []string) {
		for _, r := range f.events.rows {
			out = append(out, string(r.Status)+"/"+string(r.Origin)+"@"+r.StartedAt.Format("15:04:05"))
		}
		return out
	}

	t.Run("drive started: the primary driver is driving, where the reading says", func(t *testing.T) {
		f := newMotionFixture(model.AssignmentPrimary)
		at := f.clock.Add(1500 * time.Millisecond)

		require.NoError(t, f.svc.RecordMotion(inTxCtx, device, change(model.DriveStarted, at)))

		require.Len(t, f.events.rows, 1)
		e := f.events.rows[0]
		assert.Equal(t, model.DutyDriving, e.Status)
		assert.Equal(t, model.DutyOriginAuto, e.Origin)
		assert.Nil(t, e.CreatedBy, "nobody typed it")
		assert.Equal(t, f.clock.Add(time.Second), e.StartedAt, "whole seconds")
		assert.Equal(t, f.truck, *e.TruckID)
		assert.Equal(t, lat, *e.Latitude)
		assert.Equal(t, 120001.0, *e.OdometerMi)
	})

	t.Run("a whole trip: on duty, drive, stop, drive again", func(t *testing.T) {
		f := newMotionFixture(model.AssignmentPrimary)
		_, _, err := f.change(model.DutyOnDuty) // 06:00 pre-trip
		require.NoError(t, err)
		t0 := f.clock

		for _, c := range []model.MotionChange{
			change(model.DriveStarted, t0.Add(15*time.Minute)),
			change(model.DriveStarted, t0.Add(20*time.Minute)), // after a red light: still the same drive
			change(model.StopConfirmed, t0.Add(4*time.Hour)),
			change(model.StopConfirmed, t0.Add(4*time.Hour)), // the sweeper and a late reading both say it
			change(model.DriveStarted, t0.Add(5*time.Hour)),
		} {
			require.NoError(t, f.svc.RecordMotion(inTxCtx, device, c))
		}

		assert.Equal(t, []string{
			"on_duty/driver@06:00:00", "driving/auto@06:15:00", "on_duty/auto@10:00:00", "driving/auto@11:00:00",
		}, statuses(f))
	})

	t.Run("a stop does not undo what the driver chose meanwhile", func(t *testing.T) {
		f := newMotionFixture(model.AssignmentPrimary)
		t0 := f.clock
		require.NoError(t, f.svc.RecordMotion(inTxCtx, device, change(model.DriveStarted, t0)))
		f.clock = t0.Add(2*time.Hour + 2*time.Minute) // stopped at 08:00, pressed off duty at 08:02
		_, _, err := f.change(model.DutyOffDuty)
		require.NoError(t, err)

		require.NoError(t, f.svc.RecordMotion(inTxCtx, device, change(model.StopConfirmed, t0.Add(2*time.Hour+5*time.Minute))))

		assert.Equal(t, []string{"driving/auto@06:00:00", "off_duty/driver@08:02:00"}, statuses(f))
	})

	t.Run("personal conveyance and yard moves are not driving", func(t *testing.T) {
		for _, c := range []struct {
			status  model.DutyStatus
			special model.SpecialDriving
		}{{model.DutyOffDuty, model.SpecialPersonalConveyance}, {model.DutyOnDuty, model.SpecialYardMoves}} {
			f := newMotionFixture(model.AssignmentPrimary)
			special := c.special
			_, _, err := f.svc.ChangeMyStatus(context.Background(), f.me, ChangeDutyStatusInput{Status: c.status, Special: &special})
			require.NoError(t, err)

			require.NoError(t, f.svc.RecordMotion(inTxCtx, device, change(model.DriveStarted, f.clock.Add(time.Minute))))

			assert.Len(t, f.events.rows, 1, string(special))
		}
	})

	t.Run("a stop ends only the ELD's driving", func(t *testing.T) {
		f := newMotionFixture(model.AssignmentPrimary)
		_, _, err := f.change(model.DutySleeperBerth)
		require.NoError(t, err)

		require.NoError(t, f.svc.RecordMotion(inTxCtx, device, change(model.StopConfirmed, f.clock.Add(time.Hour))))

		assert.Len(t, f.events.rows, 1, "a sleeping driver is not woken up to on duty")
	})

	t.Run("nobody at the wheel, or no truck: nothing", func(t *testing.T) {
		f := newMotionFixture(model.AssignmentCoDriver)
		require.NoError(t, f.svc.RecordMotion(inTxCtx, device, change(model.DriveStarted, f.clock)))
		assert.Empty(t, f.events.rows, "the co-driver is never driving")

		require.NoError(t, f.svc.RecordMotion(inTxCtx, uuid.New(), change(model.DriveStarted, f.clock)))
		assert.Empty(t, f.events.rows, "a device installed nowhere")
	})

	t.Run("a driver's own change in the same second wins", func(t *testing.T) {
		f := newMotionFixture(model.AssignmentPrimary)
		_, _, err := f.change(model.DutyOnDuty)
		require.NoError(t, err)

		err = f.svc.RecordMotion(inTxCtx, device, change(model.DriveStarted, f.clock.Add(300*time.Millisecond)))

		require.NoError(t, err, "no unique violation: it would abort the whole ingest transaction")
		assert.Len(t, f.events.rows, 1)
	})
}

func TestDutyStatusService_DailyLog(t *testing.T) {
	ctx := context.Background()
	inTxCtx := context.WithValue(ctx, inTxKey{}, true)
	f := newDutyFixture()
	// f.clock is 2026-11-11 06:00 UTC = 00:00 in Chicago: Nov 10 is over.
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	local := func(day, h, m int) time.Time { return time.Date(2026, 11, day, h, m, 0, 0, chicago) }
	for _, e := range []struct {
		s  model.DutyStatus
		at time.Time
	}{
		{model.DutyOffDuty, local(9, 20, 0)},
		{model.DutyOnDuty, local(10, 6, 0)},
		{model.DutyDriving, local(10, 7, 0)},
		{model.DutyOffDuty, local(10, 18, 0)},
	} {
		origin := model.DutyOriginDriver
		if e.s == model.DutyDriving {
			origin = model.DutyOriginAuto
		}
		_, err := f.events.Insert(inTxCtx, model.DutyStatusEvent{CompanyID: f.driver.CompanyID, DriverID: f.driver.ID,
			Status: e.s, StartedAt: e.at.UTC(), Origin: origin})
		require.NoError(t, err)
	}

	t.Run("the driver's own day, in the company's time zone", func(t *testing.T) {
		log, err := f.svc.MyDailyLog(ctx, f.me, "2026-11-10")

		require.NoError(t, err)
		assert.Equal(t, f.driver.ID, log.DriverID)
		assert.Equal(t, "2026-11-10", log.Date)
		assert.Equal(t, "America/Chicago", log.Location.String())
		assert.True(t, log.Start.Equal(local(10, 0, 0)), "Chicago midnight, not UTC")
		assert.Len(t, log.Segments, 4)
		assert.Equal(t, 11*time.Hour, log.Totals[model.DutyDriving])
		assert.Equal(t, 12*time.Hour, log.Totals[model.DutyOffDuty])
		assert.Equal(t, time.Hour, log.Totals[model.DutyOnDuty])
	})

	t.Run("the office reads any driver of its company", func(t *testing.T) {
		office := auth.Principal{UserID: uuid.New(), CompanyID: f.me.CompanyID}
		log, err := f.svc.DriverDailyLog(ctx, office, f.driver.ID, "2026-11-10")
		require.NoError(t, err)
		assert.Len(t, log.Segments, 4)

		other := uuid.New()
		_, err = f.svc.DriverDailyLog(ctx, auth.Principal{UserID: uuid.New(), CompanyID: &other}, f.driver.ID, "2026-11-10")
		assert.ErrorIs(t, err, model.ErrNotFound, "another company's driver does not exist")

		_, err = f.svc.DriverDailyLog(ctx, auth.Principal{UserID: uuid.New()}, f.driver.ID, "2026-11-10")
		assert.NoError(t, err, "platform staff see every company")
	})

	t.Run("today so far", func(t *testing.T) {
		f.clock = local(11, 2, 30).UTC()
		t.Cleanup(func() { f.clock = local(11, 0, 0).UTC() })

		log, err := f.svc.MyDailyLog(ctx, f.me, "2026-11-11")

		require.NoError(t, err)
		assert.True(t, log.Until.Equal(local(11, 2, 30)))
		assert.Equal(t, 2*time.Hour+30*time.Minute, log.Totals[model.DutyOffDuty], "off since yesterday 18:00")
	})

	t.Run("refused dates", func(t *testing.T) {
		for date, msg := range map[string]string{
			"2026-11-11": "is in the future", // Chicago's Nov 11 begins exactly now: nothing to show yet
			"2026-12-01": "is in the future",
			"10.11.2026": "must be a date like 2026-11-10",
			"":           "must be a date like 2026-11-10",
		} {
			_, err := f.svc.MyDailyLog(ctx, f.me, date)
			var vErr *validation.ValidationError
			require.ErrorAs(t, err, &vErr, date)
			assert.Equal(t, msg, vErr.Fields["date"], date)
		}
	})
}

// editFixture: yesterday (Nov 10, all UTC) as the ELD and the driver left
// it, and the driver forgot to go off duty after unloading.
//
//	10:00 on_duty (driver)   pre-trip
//	11:00 driving (auto)
//	15:00 on_duty (auto)     stopped 5 minutes: unloading
type editFixture struct {
	*dutyFixture
	preTrip, drive, unload model.DutyStatusEvent
	inTx                   context.Context
}

func newEditFixture(t *testing.T) *editFixture {
	t.Helper()
	f := &editFixture{dutyFixture: newDutyFixture(), inTx: context.WithValue(context.Background(), inTxKey{}, true)}
	f.board(model.AssignmentPrimary)
	add := func(s model.DutyStatus, h int, origin model.DutyOrigin) model.DutyStatusEvent {
		e, err := f.events.Insert(f.inTx, model.DutyStatusEvent{CompanyID: f.driver.CompanyID, DriverID: f.driver.ID,
			Status: s, StartedAt: yesterday(h, 0), Origin: origin, TruckID: &f.truck})
		require.NoError(t, err)
		return *e
	}
	f.preTrip = add(model.DutyOnDuty, 10, model.DutyOriginDriver)
	f.drive = add(model.DutyDriving, 11, model.DutyOriginAuto)
	f.unload = add(model.DutyOnDuty, 15, model.DutyOriginAuto)
	return f
}

func yesterday(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }

func record(s model.DutyStatus, at time.Time) DutyRecordInput {
	return DutyRecordInput{Status: s, StartedAt: at, Reason: "forgot to press the button"}
}

func (f *editFixture) statuses() (out []string) {
	for _, r := range f.events.rows {
		if active(r) {
			out = append(out, string(r.Status)+"@"+r.StartedAt.Format("15:04"))
		}
	}
	return out
}

func TestDutyStatusService_AddMyRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("the forgotten off duty", func(t *testing.T) {
		f := newEditFixture(t)

		e, err := f.svc.AddMyRecord(ctx, f.me, record(model.DutyOffDuty, yesterday(16, 0)))

		require.NoError(t, err)
		assert.Equal(t, model.DutyOriginDriver, e.Origin)
		assert.Equal(t, "forgot to press the button", *e.EditReason)
		assert.Nil(t, e.Replaces, "added, not a new version of anything")
		assert.Equal(t, []string{"on_duty@10:00", "driving@11:00", "on_duty@15:00", "off_duty@16:00"}, f.statuses())
	})

	t.Run("never inside a drive", func(t *testing.T) {
		f := newEditFixture(t)

		_, err := f.svc.AddMyRecord(ctx, f.me, record(model.DutyOffDuty, yesterday(13, 0)))

		assert.ErrorIs(t, err, model.ErrEditChangesDriving, "it would cut 11:00-15:00 driving in two")
		assert.Len(t, f.statuses(), 3)
	})

	t.Run("not at an instant already taken", func(t *testing.T) {
		f := newEditFixture(t)
		_, err := f.svc.AddMyRecord(ctx, f.me, record(model.DutySleeperBerth, yesterday(10, 0)))
		assert.ErrorIs(t, err, model.ErrDutyStatusTaken)
	})

	t.Run("not while driving now: that would end the drive", func(t *testing.T) {
		f := newEditFixture(t)
		f.clock = yesterday(12, 0) // the drive is still going

		_, err := f.svc.AddMyRecord(ctx, f.me, record(model.DutyOffDuty, yesterday(11, 30)))

		assert.ErrorIs(t, err, model.ErrEditChangesDriving)
	})
}

func TestDutyStatusService_ChangeMyRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("relabel: the old version stays as history", func(t *testing.T) {
		f := newEditFixture(t)

		e, err := f.svc.ChangeMyRecord(ctx, f.me, f.unload.ID, record(model.DutyOffDuty, f.unload.StartedAt))

		require.NoError(t, err)
		assert.Equal(t, f.unload.ID, *e.Replaces)
		assert.Equal(t, model.DutyOriginDriver, e.Origin, "the driver's version now")
		assert.Equal(t, f.truck, *e.TruckID, "same moment, same place")
		assert.Equal(t, []string{"on_duty@10:00", "driving@11:00", "off_duty@15:00"}, f.statuses())

		old, err := f.events.GetByID(ctx, f.unload.ID)
		require.NoError(t, err)
		assert.Equal(t, model.RecordInactive, old.RecordStatus)
		assert.Equal(t, model.DutyOnDuty, old.Status, "untouched")
		assert.Equal(t, f.me.UserID, *old.InactivatedBy)
	})

	t.Run("moving a record that does not touch driving", func(t *testing.T) {
		f := newEditFixture(t)

		_, err := f.svc.ChangeMyRecord(ctx, f.me, f.preTrip.ID, record(model.DutyOnDuty, yesterday(9, 30)))

		require.NoError(t, err)
		assert.Equal(t, []string{"on_duty@09:30", "driving@11:00", "on_duty@15:00"}, f.statuses())
	})

	t.Run("every way of changing the drive is refused", func(t *testing.T) {
		for name, in := range map[string]DutyRecordInput{
			"the stop moved earlier: shorter drive":   record(model.DutyOnDuty, yesterday(14, 0)),
			"the stop moved later: longer drive":      record(model.DutyOnDuty, yesterday(16, 0)),
			"the pre-trip moved into the drive":       record(model.DutyOnDuty, yesterday(11, 30)),
			"the pre-trip moved onto the drive start": record(model.DutyOnDuty, yesterday(11, 0)),
		} {
			t.Run(name, func(t *testing.T) {
				f := newEditFixture(t)
				target := f.unload
				if strings.HasPrefix(name, "the pre-trip") {
					target = f.preTrip
				}

				_, err := f.svc.ChangeMyRecord(ctx, f.me, target.ID, in)

				require.Error(t, err)
				assert.True(t, errors.Is(err, model.ErrEditChangesDriving) || errors.Is(err, model.ErrDutyStatusTaken), err)
				assert.Equal(t, []string{"on_duty@10:00", "driving@11:00", "on_duty@15:00"}, f.statuses(), "nothing changed")
			})
		}
	})

	t.Run("driving itself is not editable", func(t *testing.T) {
		f := newEditFixture(t)
		_, err := f.svc.ChangeMyRecord(ctx, f.me, f.drive.ID, record(model.DutyOnDuty, f.drive.StartedAt))
		assert.ErrorIs(t, err, model.ErrDrivingNotEditable)
	})

	t.Run("only the newest version", func(t *testing.T) {
		f := newEditFixture(t)
		_, err := f.svc.ChangeMyRecord(ctx, f.me, f.unload.ID, record(model.DutyOffDuty, f.unload.StartedAt))
		require.NoError(t, err)

		_, err = f.svc.ChangeMyRecord(ctx, f.me, f.unload.ID, record(model.DutySleeperBerth, f.unload.StartedAt))

		assert.ErrorIs(t, err, model.ErrRecordReplaced)
	})

	t.Run("only your own records", func(t *testing.T) {
		f := newEditFixture(t)
		// Bob drives for the same company; his record is in the same store.
		bob := model.Driver{ID: uuid.New(), UserID: uuid.New(), CompanyID: f.driver.CompanyID}
		f.events.drivers.byUser[bob.UserID] = bob
		bobs, err := f.events.Insert(f.inTx, model.DutyStatusEvent{CompanyID: bob.CompanyID, DriverID: bob.ID,
			Status: model.DutyOffDuty, StartedAt: yesterday(12, 0), Origin: model.DutyOriginDriver})
		require.NoError(t, err)

		_, err = f.svc.ChangeMyRecord(ctx, f.me, bobs.ID, record(model.DutySleeperBerth, bobs.StartedAt))
		assert.ErrorIs(t, err, model.ErrNotFound, "Bob's record does not exist for Ali")

		stranger := auth.Principal{UserID: uuid.New(), CompanyID: f.me.CompanyID}
		_, err = f.svc.ChangeMyRecord(ctx, stranger, f.preTrip.ID, record(model.DutyOffDuty, f.preTrip.StartedAt))
		assert.ErrorIs(t, err, model.ErrNotFound, "not a driver")
	})

	t.Run("refused input", func(t *testing.T) {
		f := newEditFixture(t)
		noReason := record(model.DutyOffDuty, f.unload.StartedAt)
		noReason.Reason = " ok "
		cases := map[string]struct {
			in    DutyRecordInput
			field string
		}{
			"nothing changes":   {record(model.DutyOnDuty, f.unload.StartedAt), "status"},
			"no real reason":    {noReason, "reason"},
			"in the future":     {record(model.DutyOffDuty, f.clock.Add(time.Minute)), "started_at"},
			"older than 8 days": {record(model.DutyOffDuty, f.clock.Add(-9*24*time.Hour)), "started_at"},
			"driving":           {record(model.DutyDriving, f.unload.StartedAt), "status"},
			"no time":           {record(model.DutyOffDuty, time.Time{}), "started_at"},
		}
		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := f.svc.ChangeMyRecord(ctx, f.me, f.unload.ID, c.in)
				var vErr *validation.ValidationError
				require.ErrorAs(t, err, &vErr)
				assert.Contains(t, vErr.Fields, c.field)
			})
		}
	})
}

func TestDutyStatusService_CertifyMyLog(t *testing.T) {
	ctx := context.Background()
	f := newEditFixture(t)
	f.clock = f.clock.Add(time.Hour) // after the records were written

	log, certified, err := f.svc.CertifyMyLog(ctx, f.me, "2026-11-10")
	require.NoError(t, err)
	assert.True(t, certified)
	assert.True(t, log.Certified())

	t.Run("signing an unchanged day again changes nothing", func(t *testing.T) {
		_, certified, err := f.svc.CertifyMyLog(ctx, f.me, "2026-11-10")
		require.NoError(t, err)
		assert.False(t, certified)
		assert.Len(t, f.events.certs, 1)
	})

	t.Run("an edit undoes the signature; the day is signed again", func(t *testing.T) {
		f.clock = f.clock.Add(time.Hour)
		_, err := f.svc.ChangeMyRecord(ctx, f.me, f.unload.ID, record(model.DutyOffDuty, f.unload.StartedAt))
		require.NoError(t, err)

		log, err := f.svc.MyDailyLog(ctx, f.me, "2026-11-10")
		require.NoError(t, err)
		assert.False(t, log.Certified(), "changed after the signature")
		require.Len(t, log.History, 1, "the replaced version is shown")
		assert.Equal(t, f.unload.ID, log.History[0].ID)

		f.clock = f.clock.Add(time.Minute)
		log, certified, err := f.svc.CertifyMyLog(ctx, f.me, "2026-11-10")
		require.NoError(t, err)
		assert.True(t, certified)
		assert.True(t, log.Certified())
		assert.Len(t, f.events.certs, 2, "append-only: both signatures are kept")
	})

	t.Run("refused days", func(t *testing.T) {
		var vErr *validation.ValidationError
		_, _, err := f.svc.CertifyMyLog(ctx, f.me, "2026-11-01")
		require.ErrorAs(t, err, &vErr)
		assert.Equal(t, "has no records to certify", vErr.Fields["date"])

		_, _, err = f.svc.CertifyMyLog(ctx, f.me, "2026-12-01")
		require.ErrorAs(t, err, &vErr)
		assert.Equal(t, "is in the future", vErr.Fields["date"])
	})
}
