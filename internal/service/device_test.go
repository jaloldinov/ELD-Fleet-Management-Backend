package service

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// memTelemetry stores seqs like the unique index: a seq stored once is
// never accepted again.
type memTelemetry struct {
	stored      map[int64]model.TelemetryPoint
	batches     int
	state       *model.TelemetryPoint // newer recorded_at wins, like the SQL
	motionSince time.Time
	inTx        bool
	toSettle    []uuid.UUID // what ListStopsToSettle answers
}

func (m *memTelemetry) GetState(context.Context, uuid.UUID) (*model.DeviceState, error) {
	if m.state == nil {
		return nil, model.ErrNotFound
	}
	return &model.DeviceState{TelemetryPoint: *m.state, Moving: m.state.Moving(), MotionSince: m.motionSince}, nil
}

func (m *memTelemetry) UpsertState(ctx context.Context, _, _ uuid.UUID, p model.TelemetryPoint, motionSince time.Time) (bool, error) {
	m.inTx = inTx(ctx)
	if m.state != nil && !m.state.RecordedAt.Before(p.RecordedAt) {
		return false, nil
	}
	m.state, m.motionSince = &p, motionSince
	return true, nil
}

func (m *memTelemetry) ListStopsToSettle(context.Context, time.Time) ([]uuid.UUID, error) {
	return m.toSettle, nil
}

// fakeDuty records what the logbook was told, and whether the device was
// locked first.
type fakeDuty struct {
	changes   []model.MotionChange
	devices   *memELDRepo
	fail      error
	failFirst error // only the first call fails
}

func (d *fakeDuty) RecordMotion(ctx context.Context, _ uuid.UUID, c model.MotionChange) error {
	if !inTx(ctx) {
		return errors.New("recorded outside the ingest transaction")
	}
	if !slices.Contains(d.devices.events, "lock") {
		return errors.New("recorded without locking the device")
	}
	if err := d.failFirst; err != nil {
		d.failFirst = nil
		return err
	}
	d.changes = append(d.changes, c)
	return d.fail
}

func (m *memTelemetry) InsertBatch(_ context.Context, _, _ uuid.UUID, points []model.TelemetryPoint) ([]int64, error) {
	m.batches++
	var accepted []int64
	for _, p := range points {
		if _, ok := m.stored[p.Seq]; !ok {
			m.stored[p.Seq] = p
			accepted = append(accepted, p.Seq)
		}
	}
	return accepted, nil
}

type deviceSvcFixture struct {
	svc       *DeviceService
	devices   *memELDRepo
	stints    *memELDAssignments
	trucks    *memTruckRepo
	telemetry *memTelemetry
	duty      *fakeDuty
}

func newDeviceSvcFixture() *deviceSvcFixture {
	f := &deviceSvcFixture{devices: newMemELDRepo(), stints: newMemELDAssignments(), trucks: newMemTruckRepo(),
		telemetry: &memTelemetry{stored: map[int64]model.TelemetryPoint{}}}
	f.duty = &fakeDuty{devices: f.devices}
	// The device the ingest tests send as (dev).
	f.devices.devices[dev.DeviceID] = &model.ELDDevice{ID: dev.DeviceID, CompanyID: dev.CompanyID, Status: model.ELDActive, IsActive: true}
	f.svc = NewDeviceService(&fakeTx{}, f.devices, f.stints, f.trucks, f.telemetry, f.duty)
	f.svc.now = func() time.Time { return testNow } // the readings below are from this afternoon
	return f
}

// withKey adds a device of companyA with a key, and returns the key.
func (f *deviceSvcFixture) withKey(t *testing.T) (*model.ELDDevice, string) {
	t.Helper()
	d := f.devices.add(companyA)
	key, hash, err := auth.NewDeviceKey()
	require.NoError(t, err)
	_, err = f.devices.SetKey(context.Background(), d.ID, hash)
	require.NoError(t, err)
	return d, key
}

func TestDeviceService_Authenticate(t *testing.T) {
	ctx := context.Background()

	t.Run("a valid key: the device, and the key is marked used", func(t *testing.T) {
		f := newDeviceSvcFixture()
		d, key := f.withKey(t)

		got, err := f.svc.Authenticate(ctx, key)

		require.NoError(t, err)
		assert.Equal(t, auth.Device{DeviceID: d.ID, CompanyID: companyA}, *got)
		assert.Equal(t, 1, f.devices.touched)
	})

	t.Run("junk is refused without a lookup", func(t *testing.T) {
		f := newDeviceSvcFixture()

		_, err := f.svc.Authenticate(ctx, "hello")

		assert.ErrorIs(t, err, model.ErrDeviceKeyInvalid)
	})

	t.Run("a well-formed but unknown key", func(t *testing.T) {
		f := newDeviceSvcFixture()
		key, _, err := auth.NewDeviceKey()
		require.NoError(t, err)

		_, err = f.svc.Authenticate(ctx, key)

		assert.ErrorIs(t, err, model.ErrDeviceKeyInvalid, "the same answer as junk: nothing to learn")
		assert.Zero(t, f.devices.touched)
	})

	t.Run("a retired device", func(t *testing.T) {
		f := newDeviceSvcFixture()
		d, key := f.withKey(t)
		d.Status = model.ELDRetired

		_, err := f.svc.Authenticate(ctx, key)

		assert.ErrorIs(t, err, model.ErrDeviceRetired)
	})

	t.Run("a faulty device may still talk", func(t *testing.T) {
		f := newDeviceSvcFixture()
		d, key := f.withKey(t)
		d.Status = model.ELDFaulty

		_, err := f.svc.Authenticate(ctx, key)

		assert.NoError(t, err, "a broken device can still report (diagnostics come in Phase 9)")
	})
}

func TestDeviceService_Me(t *testing.T) {
	ctx := context.Background()
	f := newDeviceSvcFixture()
	d, _ := f.withKey(t)
	dev := auth.Device{DeviceID: d.ID, CompanyID: companyA}

	self, err := f.svc.Me(ctx, dev)
	require.NoError(t, err)
	assert.Equal(t, d.ID, self.Device.ID)
	assert.Nil(t, self.Truck, "not installed")

	tr := f.trucks.add(companyA)
	_, err = f.stints.Open(ctx, model.ELDAssignment{CompanyID: companyA, ELDDeviceID: d.ID, TruckID: tr.ID})
	require.NoError(t, err)

	self, err = f.svc.Me(ctx, dev)
	require.NoError(t, err)
	require.NotNil(t, self.Truck)
	assert.Equal(t, tr.ID, self.Truck.ID)
	require.NotNil(t, self.InstalledAt, "when the stint began")

	_, err = f.svc.Me(ctx, auth.Device{DeviceID: uuid.New()})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// ---------- ingest ----------

func goodPoint(seq int64) TelemetryInput {
	at := time.Date(2026, 11, 8, 14, 30, 0, 0, time.UTC)
	lat, lon := 32.7767, -96.797
	speed, odo, hours, on := 61.5, 120345.2, 8123.4, true
	return TelemetryInput{Seq: &seq, RecordedAt: &at, Latitude: &lat, Longitude: &lon,
		SpeedMPH: &speed, OdometerMi: &odo, EngineHours: &hours, EngineOn: &on}
}

var dev = auth.Device{DeviceID: uuid.New(), CompanyID: companyA}

// testNow is "our clock" in these tests: goodPoint is recorded at 14:30.
var testNow = time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)

func TestDeviceService_Ingest(t *testing.T) {
	ctx := context.Background()

	t.Run("a clean batch", func(t *testing.T) {
		f := newDeviceSvcFixture()

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{goodPoint(1), goodPoint(2), goodPoint(3)}})

		require.NoError(t, err)
		assert.Equal(t, IngestResult{Accepted: 3}, *res)
		assert.Equal(t, 1, f.telemetry.batches, "one database call for the batch")
	})

	t.Run("good, bad and duplicate points together: each gets its own answer", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{goodPoint(1)}})
		require.NoError(t, err)

		badLat := goodPoint(4)
		lat := 91.0
		badLat.Latitude = &lat
		noSpeed := goodPoint(5)
		noSpeed.SpeedMPH = nil

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{
			goodPoint(1), // stored by the earlier request
			goodPoint(2),
			goodPoint(2), // twice in this batch
			badLat,
			noSpeed,
			goodPoint(6),
		}})

		require.NoError(t, err, "bad points do not fail the batch")
		assert.Equal(t, 2, res.Accepted, "2 and 6")
		assert.Equal(t, 2, res.Duplicates, "1 (earlier request) and the second 2")
		require.Len(t, res.Rejected, 2)
		assert.Equal(t, 3, res.Rejected[0].Index)
		assert.Equal(t, int64(4), *res.Rejected[0].Seq)
		assert.Equal(t, "latitude: must be between -90 and 90", res.Rejected[0].Reason)
		assert.Equal(t, "speed_mph: is required", res.Rejected[1].Reason)
		assert.NotContains(t, f.telemetry.stored, int64(4))
	})

	t.Run("sending the same batch again: all duplicates", func(t *testing.T) {
		f := newDeviceSvcFixture()
		batch := []TelemetryInput{goodPoint(1), goodPoint(2)}
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: batch})
		require.NoError(t, err)

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: batch})

		require.NoError(t, err)
		assert.Equal(t, IngestResult{Duplicates: 2}, *res, "a lost reply is safe to retry")
	})

	t.Run("bad points one by one", func(t *testing.T) {
		cases := map[string]struct {
			edit   func(p *TelemetryInput)
			reason string
		}{
			"no seq":          {func(p *TelemetryInput) { p.Seq = nil }, "seq: is required"},
			"seq zero":        {func(p *TelemetryInput) { z := int64(0); p.Seq = &z }, "seq: must be 1 or more"},
			"no time":         {func(p *TelemetryInput) { p.RecordedAt = nil }, "recorded_at: is required"},
			"half a position": {func(p *TelemetryInput) { p.Longitude = nil }, "latitude: latitude and longitude come together (or neither: no GPS fix)"},
			"negative speed":  {func(p *TelemetryInput) { s := -1.0; p.SpeedMPH = &s }, "speed_mph: must be between 0 and 150"},
			"NaN odometer":    {func(p *TelemetryInput) { n := math.NaN(); p.OdometerMi = &n }, "odometer_mi: must be between 0 and 10000000"},
			"several at once": {func(p *TelemetryInput) { p.EngineOn, p.EngineHours = nil, nil },
				"engine_hours: is required; engine_on: is required"},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				f := newDeviceSvcFixture()
				p := goodPoint(7)
				tc.edit(&p)

				res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{p}})

				require.NoError(t, err)
				require.Len(t, res.Rejected, 1)
				assert.Equal(t, tc.reason, res.Rejected[0].Reason)
				assert.Zero(t, f.telemetry.batches, "nothing valid: the database is not called")
			})
		}
	})

	t.Run("no GPS fix is fine", func(t *testing.T) {
		f := newDeviceSvcFixture()
		p := goodPoint(1)
		p.Latitude, p.Longitude = nil, nil

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{p}})

		require.NoError(t, err)
		assert.Equal(t, 1, res.Accepted)
	})

	t.Run("an unusable request as a whole", func(t *testing.T) {
		f := newDeviceSvcFixture()

		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: nil})
		requireValidationField(t, err, "events")

		big := make([]TelemetryInput, MaxTelemetryBatch+1)
		for i := range big {
			big[i] = goodPoint(int64(i + 1))
		}
		_, err = f.svc.Ingest(ctx, dev, TelemetryBatch{Points: big})
		requireValidationField(t, err, "events")
	})
}

// ---------- last known state ----------

func pointAt(seq int64, minute int) TelemetryInput {
	p := goodPoint(seq)
	at := time.Date(2026, 11, 8, 14, minute, 0, 0, time.UTC)
	p.RecordedAt = &at
	return p
}

func TestDeviceService_Ingest_State(t *testing.T) {
	ctx := context.Background()

	t.Run("the newest accepted point becomes the state, in the batch's transaction", func(t *testing.T) {
		f := newDeviceSvcFixture()

		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{pointAt(2, 31), pointAt(3, 32), pointAt(1, 30)}})

		require.NoError(t, err)
		require.NotNil(t, f.telemetry.state)
		assert.Equal(t, int64(3), f.telemetry.state.Seq, "newest by the device's clock, whatever the order in the batch")
		assert.True(t, f.telemetry.inTx, "points and state: together or not at all")
	})

	t.Run("a late, older batch does not move the truck back", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{pointAt(10, 50)}})
		require.NoError(t, err)

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{pointAt(5, 20), pointAt(6, 21)}}) // from the tunnel

		require.NoError(t, err)
		assert.Equal(t, 2, res.Accepted, "the history gets them")
		assert.Equal(t, int64(10), f.telemetry.state.Seq, "the state keeps the newer reading")
	})

	t.Run("only duplicates: the state is not touched", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{pointAt(1, 30)}})
		require.NoError(t, err)
		f.telemetry.state = nil

		_, err = f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{pointAt(1, 30)}})

		require.NoError(t, err)
		assert.Nil(t, f.telemetry.state)
	})
}

func TestNewerThanState(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 8, 14, m, 0, 0, time.UTC) }
	points := []model.TelemetryPoint{{Seq: 4, RecordedAt: at(9)}, {Seq: 3, RecordedAt: at(5)}, {Seq: 2, RecordedAt: at(5)}, {Seq: 1, RecordedAt: at(1)}}
	seqs := func(ps []model.TelemetryPoint) (out []int64) {
		for _, p := range ps {
			out = append(out, p.Seq)
		}
		return out
	}

	assert.Equal(t, []int64{1, 2, 3}, seqs(newerThanState(points, []int64{1, 2, 3}, nil)),
		"oldest first, same time by seq; seq 4 was not accepted")
	state := &model.DeviceState{TelemetryPoint: model.TelemetryPoint{RecordedAt: at(5)}}
	assert.Equal(t, []int64{4}, seqs(newerThanState(points, []int64{1, 2, 3, 4}, state)),
		"not newer than the state: history only")
	assert.Empty(t, newerThanState(points, nil, nil))
}

// ---------- time and order rules ----------

func at(t time.Time) TelemetryInput {
	p := goodPoint(1)
	p.RecordedAt = &t
	return p
}

func withSeq(p TelemetryInput, seq int64) TelemetryInput {
	p.Seq = &seq
	return p
}

func TestDeviceService_Ingest_TimeRules(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		recorded time.Time
		sentAt   *time.Time
		reason   string // "" = accepted
	}{
		"9 minutes ahead: within the FMCSA 10": {recorded: testNow.Add(9 * time.Minute)},
		"11 minutes ahead": {recorded: testNow.Add(11 * time.Minute),
			reason: "recorded_at: more than 10 minutes in the future (the device clock is wrong: timing malfunction)"},
		"29 days old":    {recorded: testNow.Add(-29 * 24 * time.Hour)},
		"31 days old":    {recorded: testNow.Add(-31 * 24 * time.Hour), reason: "recorded_at: older than 30 days"},
		"before sent_at": {recorded: testNow.Add(-time.Minute), sentAt: &testNow},
		"after sent_at": {recorded: testNow.Add(-time.Minute), sentAt: ptrTime(testNow.Add(-5 * time.Minute)),
			reason: "recorded_at: after the batch's sent_at (recorded after it was sent?)"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDeviceSvcFixture()

			res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{SentAt: tc.sentAt, Points: []TelemetryInput{at(tc.recorded)}})

			require.NoError(t, err)
			if tc.reason == "" {
				assert.Equal(t, 1, res.Accepted)
				return
			}
			require.Len(t, res.Rejected, 1)
			assert.Equal(t, tc.reason, res.Rejected[0].Reason)
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestDeviceService_Ingest_ClockOffset(t *testing.T) {
	ctx := context.Background()
	f := newDeviceSvcFixture()
	d, _ := f.withKey(t)
	me := auth.Device{DeviceID: d.ID, CompanyID: companyA}
	fast := testNow.Add(7 * time.Minute) // the device's clock is 7 minutes fast

	res, err := f.svc.Ingest(ctx, me, TelemetryBatch{SentAt: &fast, Points: []TelemetryInput{at(testNow.Add(20 * time.Minute))}})

	require.NoError(t, err)
	assert.Len(t, res.Rejected, 1, "the point is too far ahead")
	require.NotNil(t, d.ClockOffset, "but the drift is recorded, exactly when the office needs it")
	assert.Equal(t, 7*time.Minute, *d.ClockOffset)
}

func TestDeviceService_Ingest_Order(t *testing.T) {
	ctx := context.Background()
	clock := func(m int) time.Time { return time.Date(2026, 11, 8, 14, m, 0, 0, time.UTC) }

	t.Run("within a batch: a higher seq must not be earlier", func(t *testing.T) {
		f := newDeviceSvcFixture()

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{
			withSeq(at(clock(40)), 3),
			withSeq(at(clock(30)), 1),
			withSeq(at(clock(20)), 2), // the clock went back after seq 1
		}})

		require.NoError(t, err)
		assert.Equal(t, 2, res.Accepted, "1 and 3: their order is fine")
		require.Len(t, res.Rejected, 1)
		assert.Equal(t, 2, res.Rejected[0].Index)
		assert.Equal(t, "recorded_at: earlier than seq 1's, which comes before it (the device clock went backwards)", res.Rejected[0].Reason)
	})

	t.Run("against what is stored", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{withSeq(at(clock(50)), 10)}})
		require.NoError(t, err)

		res, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{
			withSeq(at(clock(40)), 11), // after seq 10 but earlier: clock went back
			withSeq(at(clock(55)), 5),  // before seq 10 but later: clock went back then
			withSeq(at(clock(20)), 4),  // a late retry: before seq 10 and earlier: fine
		}})

		require.NoError(t, err)
		assert.Equal(t, 1, res.Accepted)
		require.Len(t, res.Rejected, 2)
		assert.Equal(t, 0, res.Rejected[0].Index, "sorted by position in the batch")
		assert.Contains(t, res.Rejected[0].Reason, "earlier than the stored seq 10's")
		assert.Contains(t, res.Rejected[1].Reason, "later than the stored seq 10's")
	})
}

// ---------- movement → logbook ----------

func speedAt(seq int64, minute int, mph float64) TelemetryInput {
	p := pointAt(seq, minute)
	p.SpeedMPH = &mph
	return p
}

func motionKinds(changes []model.MotionChange) (out []string) {
	for _, c := range changes {
		out = append(out, string(c.Kind)+"@"+c.At.Format("15:04"))
	}
	return out
}

func TestDeviceService_Ingest_Motion(t *testing.T) {
	ctx := context.Background()

	t.Run("the batch's movement reaches the logbook, oldest first", func(t *testing.T) {
		f := newDeviceSvcFixture()

		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{
			speedAt(3, 12, 0), speedAt(1, 0, 0), speedAt(2, 1, 55), speedAt(4, 18, 0), // shuffled
		}})

		require.NoError(t, err)
		assert.Equal(t, []string{"drive_started@14:01", "stop_confirmed@14:17"}, motionKinds(f.duty.changes))
		assert.Equal(t, time.Date(2026, 11, 8, 14, 12, 0, 0, time.UTC), f.telemetry.motionSince,
			"the state keeps when the stop began")
	})

	t.Run("the next batch continues the stored run", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(1, 0, 55), speedAt(2, 10, 0)}})
		require.NoError(t, err)
		f.duty.changes = nil

		_, err = f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(3, 14, 0), speedAt(4, 16, 0)}})

		require.NoError(t, err)
		assert.Equal(t, []string{"stop_confirmed@14:15"}, motionKinds(f.duty.changes),
			"the stop began in the batch before")
	})

	t.Run("points sent late are history: they change nothing now", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(10, 50, 0)}})
		require.NoError(t, err)
		f.duty.changes = nil

		_, err = f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(5, 20, 60)}}) // from the tunnel

		require.NoError(t, err)
		assert.Empty(t, f.duty.changes)
	})

	t.Run("the logbook failing fails the batch: readings and statuses together", func(t *testing.T) {
		f := newDeviceSvcFixture()
		f.duty.fail = errors.New("database is down")

		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(1, 0, 55)}})

		assert.ErrorIs(t, err, f.duty.fail, "the device gets an error and sends again")
	})
}

func TestDeviceService_SettleStops(t *testing.T) {
	ctx := context.Background()

	t.Run("a quiet, parked truck gets its stop confirmed at the mark", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(1, 0, 55), speedAt(2, 10, 0), speedAt(3, 11, 0)}})
		require.NoError(t, err)
		f.duty.changes = nil
		f.telemetry.toSettle = []uuid.UUID{dev.DeviceID}
		f.svc.now = func() time.Time { return time.Date(2026, 11, 8, 14, 20, 0, 0, time.UTC) }

		n, err := f.svc.SettleStops(ctx)

		require.NoError(t, err)
		assert.Equal(t, 1, n)
		require.Len(t, f.duty.changes, 1)
		assert.Equal(t, "stop_confirmed@14:15", motionKinds(f.duty.changes)[0])
		assert.Equal(t, int64(3), f.duty.changes[0].Point.Seq, "where the truck stands: its last reading")
	})

	t.Run("re-checked under the lock: a reading that came meanwhile wins", func(t *testing.T) {
		f := newDeviceSvcFixture()
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(1, 0, 55), speedAt(2, 10, 0), speedAt(3, 12, 60)}})
		require.NoError(t, err)
		f.duty.changes = nil
		f.telemetry.toSettle = []uuid.UUID{dev.DeviceID} // listed while still standing
		f.svc.now = func() time.Time { return time.Date(2026, 11, 8, 14, 30, 0, 0, time.UTC) }

		n, err := f.svc.SettleStops(ctx)

		require.NoError(t, err)
		assert.Zero(t, n)
		assert.Empty(t, f.duty.changes, "it is moving again")
	})

	t.Run("one device failing does not stop the others", func(t *testing.T) {
		f := newDeviceSvcFixture()
		other := f.devices.add(companyA)
		// The fake keeps one state for all devices: both stand since 14:00.
		_, err := f.svc.Ingest(ctx, dev, TelemetryBatch{Points: []TelemetryInput{speedAt(1, 0, 0)}})
		require.NoError(t, err)
		f.duty.changes = nil
		f.duty.failFirst = errors.New("deadlock detected")
		f.telemetry.toSettle = []uuid.UUID{other.ID, dev.DeviceID}
		f.svc.now = func() time.Time { return time.Date(2026, 11, 8, 14, 30, 0, 0, time.UTC) }

		n, err := f.svc.SettleStops(ctx)

		assert.ErrorContains(t, err, "deadlock detected")
		assert.ErrorContains(t, err, other.ID.String(), "says which device")
		assert.Equal(t, 1, n, "the second one was settled")
	})
}
