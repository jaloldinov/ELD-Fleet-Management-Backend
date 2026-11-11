package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// The questions the automatic driving status asks: which truck was the
// device in, who was at its wheel, and which stopped trucks still have a
// driver "driving".
func TestAutoDutyQueries(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	devices := NewELDDeviceRepository(f.db)
	installs := NewELDAssignmentRepository(f.db)
	telemetry := NewTelemetryRepository(f.db)
	duty := NewDutyStatusRepository(f.db)

	dev, err := devices.Create(ctx, model.ELDDevice{CompanyID: f.alpha.ID, SerialNumber: "PT30-0001"})
	require.NoError(t, err)
	before := time.Now().UTC().Add(-time.Second)
	_, err = installs.Open(ctx, model.ELDAssignment{CompanyID: f.alpha.ID, ELDDeviceID: dev.ID, TruckID: f.t1.ID})
	require.NoError(t, err)
	_, err = f.open(f.ali, f.t1, model.AssignmentPrimary)
	require.NoError(t, err)
	_, err = f.open(f.bob, f.t1, model.AssignmentCoDriver)
	require.NoError(t, err)
	// Readings and statuses from a minute on: after the stints began.
	base := time.Now().UTC().Add(time.Minute).Truncate(time.Second)

	t.Run("truck at", func(t *testing.T) {
		got, err := installs.TruckAt(ctx, dev.ID, base)
		require.NoError(t, err)
		assert.Equal(t, f.t1.ID, got)

		_, err = installs.TruckAt(ctx, dev.ID, before)
		assert.ErrorIs(t, err, model.ErrNotFound, "not installed yet then")
	})

	t.Run("primary at: the co-driver is never at the wheel", func(t *testing.T) {
		got, err := f.assignments.PrimaryAt(ctx, f.t1.ID, base)
		require.NoError(t, err)
		assert.Equal(t, f.ali, got.DriverID)

		_, err = f.assignments.PrimaryAt(ctx, f.t2.ID, base)
		assert.ErrorIs(t, err, model.ErrNotFound, "nobody drives truck 102")
	})

	t.Run("stops to settle", func(t *testing.T) {
		stopAt := base.Add(time.Hour)
		due := stopAt.Add(model.AutoOnDutyAfterStop)
		// Ali drives from base; the truck stops at stopAt; the last reading
		// is a minute after the stop (then the engine went off).
		_, err := duty.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
			Status: model.DutyDriving, StartedAt: base, Origin: model.DutyOriginAuto})
		require.NoError(t, err)
		_, err = telemetry.UpsertState(ctx, f.alpha.ID, dev.ID, point(1, stopAt.Add(time.Minute), 0), stopAt)
		require.NoError(t, err)

		list := func(now time.Time) []uuid.UUID {
			t.Helper()
			ids, err := telemetry.ListStopsToSettle(ctx, now)
			require.NoError(t, err)
			return ids
		}

		assert.Empty(t, list(due.Add(-time.Second)), "not 5 minutes yet")
		assert.Equal(t, []uuid.UUID{dev.ID}, list(due), "standing 5 minutes, Ali still driving")

		// Once Ali is on duty at the mark (what settling writes), it is done.
		_, err = duty.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
			Status: model.DutyOnDuty, StartedAt: due, Origin: model.DutyOriginAuto})
		require.NoError(t, err)
		assert.Empty(t, list(due.Add(time.Hour)), "settled: a parked truck stops matching")
	})

	t.Run("a reading after the mark: ingest confirmed it, not the sweeper", func(t *testing.T) {
		stopAt := base.Add(3 * time.Hour)
		_, err := duty.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
			Status: model.DutyDriving, StartedAt: stopAt.Add(-time.Hour), Origin: model.DutyOriginAuto})
		require.NoError(t, err)
		_, err = telemetry.UpsertState(ctx, f.alpha.ID, dev.ID, point(2, stopAt.Add(6*time.Minute), 0), stopAt)
		require.NoError(t, err)

		ids, err := telemetry.ListStopsToSettle(ctx, stopAt.Add(time.Hour))
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("a moving truck has no stop", func(t *testing.T) {
		at := base.Add(5 * time.Hour)
		_, err := telemetry.UpsertState(ctx, f.alpha.ID, dev.ID, point(3, at, 60), at)
		require.NoError(t, err)

		ids, err := telemetry.ListStopsToSettle(ctx, at.Add(time.Hour))
		require.NoError(t, err)
		assert.Empty(t, ids)
	})
}
