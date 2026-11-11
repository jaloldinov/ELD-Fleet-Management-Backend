package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func TestDutyStatusRepository(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	repo := NewDutyStatusRepository(f.db)
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

	add := func(status model.DutyStatus, at time.Time) *model.DutyStatusEvent {
		t.Helper()
		origin := model.DutyOriginDriver
		if status == model.DutyDriving {
			origin = model.DutyOriginAuto
		}
		e, err := repo.Insert(ctx, model.DutyStatusEvent{
			CompanyID: f.alpha.ID, DriverID: f.ali, Status: status, StartedAt: at, Origin: origin,
		})
		require.NoError(t, err)
		return e
	}

	// Yesterday 20:00 off duty, then today: 06:00 on duty, 07:00 driving,
	// 11:00 off duty; tomorrow 01:00 sleeper.
	add(model.DutyOffDuty, day.Add(-4*time.Hour))
	add(model.DutyOnDuty, day.Add(6*time.Hour))
	add(model.DutyDriving, day.Add(7*time.Hour))
	add(model.DutyOffDuty, day.Add(11*time.Hour))
	add(model.DutySleeperBerth, day.Add(25*time.Hour))

	t.Run("insert returns the stored row", func(t *testing.T) {
		lat, lon, odo := 32.7767, -96.797, 120500.5
		note := "pre-trip inspection"
		e, err := repo.Insert(ctx, model.DutyStatusEvent{
			CompanyID: f.alpha.ID, DriverID: f.bob, Status: model.DutyOnDuty, StartedAt: day,
			Origin: model.DutyOriginDriver, TruckID: &f.t1.ID, Latitude: &lat, Longitude: &lon,
			OdometerMi: &odo, Note: &note,
		})
		require.NoError(t, err)
		assert.NotEqual(t, "", e.ID.String())
		assert.Equal(t, model.DutyOnDuty, e.Status)
		assert.Nil(t, e.Special)
		assert.Equal(t, f.t1.ID, *e.TruckID)
		assert.Equal(t, odo, *e.OdometerMi)
		assert.Nil(t, e.EngineHours, "unknown stays unknown")
		assert.Equal(t, note, *e.Note)
		assert.False(t, e.CreatedAt.IsZero())
	})

	t.Run("at: the last event started at or before the instant", func(t *testing.T) {
		got, err := repo.At(ctx, f.ali, day.Add(9*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, model.DutyDriving, got.Status)

		got, err = repo.At(ctx, f.ali, day.Add(7*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, model.DutyDriving, got.Status, "an event is in force from its first instant")

		_, err = repo.At(ctx, f.ali, day.Add(-5*time.Hour))
		assert.ErrorIs(t, err, model.ErrNotFound, "before the first event")
	})

	t.Run("period: includes the event already in force at the start", func(t *testing.T) {
		events, err := repo.ListPeriod(ctx, f.ali, day, day.Add(24*time.Hour))
		require.NoError(t, err)

		var got []model.DutyStatus
		for _, e := range events {
			got = append(got, e.Status)
		}
		assert.Equal(t, []model.DutyStatus{
			model.DutyOffDuty, // from yesterday 20:00: covers today 00:00-06:00
			model.DutyOnDuty, model.DutyDriving, model.DutyOffDuty,
		}, got, "oldest first; tomorrow's sleeper is outside")
		assert.Equal(t, day.Add(-4*time.Hour), events[0].StartedAt.UTC())
	})

	t.Run("period: an event exactly at the start is not listed twice", func(t *testing.T) {
		events, err := repo.ListPeriod(ctx, f.ali, day.Add(6*time.Hour), day.Add(8*time.Hour))
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, model.DutyOnDuty, events[0].Status)
		assert.Equal(t, model.DutyDriving, events[1].Status)
	})

	t.Run("period: nothing before the driver's first event", func(t *testing.T) {
		events, err := repo.ListPeriod(ctx, f.dan, day, day.Add(24*time.Hour))
		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("one status per instant", func(t *testing.T) {
		_, err := repo.Insert(ctx, model.DutyStatusEvent{
			CompanyID: f.alpha.ID, DriverID: f.ali, Status: model.DutySleeperBerth,
			StartedAt: day.Add(6 * time.Hour), Origin: model.DutyOriginDriver,
		})
		assert.ErrorIs(t, err, model.ErrDutyStatusTaken)
	})

	t.Run("the database refuses what the rules forbid", func(t *testing.T) {
		pc, ym := model.SpecialPersonalConveyance, model.SpecialYardMoves
		authorID := f.t1.ID // any uuid: the CHECK refuses before the user is looked up
		cases := []struct {
			name       string
			event      model.DutyStatusEvent
			constraint string // the rule that must refuse it
		}{
			{"driving typed in by the driver", model.DutyStatusEvent{Status: model.DutyDriving, Origin: model.DutyOriginDriver}, "dse_driving_is_auto"},
			{"personal conveyance on duty", model.DutyStatusEvent{Status: model.DutyOnDuty, Special: &pc, Origin: model.DutyOriginDriver}, "dse_special"},
			{"yard moves off duty", model.DutyStatusEvent{Status: model.DutyOffDuty, Special: &ym, Origin: model.DutyOriginDriver}, "dse_special"},
			{"unknown status", model.DutyStatusEvent{Status: "resting", Origin: model.DutyOriginDriver}, "dse_status"},
			{"auto event with an author", model.DutyStatusEvent{Status: model.DutyDriving, Origin: model.DutyOriginAuto, CreatedBy: &authorID}, "dse_auto_has_no_author"},
			{"another company's truck", model.DutyStatusEvent{Status: model.DutyOnDuty, Origin: model.DutyOriginDriver, TruckID: &f.t1.ID, CompanyID: f.bravo.ID, DriverID: f.dan}, "dse_truck_company_fkey"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				e := c.event
				if e.CompanyID == uuid.Nil {
					e.CompanyID, e.DriverID = f.alpha.ID, f.ali
				}
				e.StartedAt = day.Add(30 * time.Hour)

				_, err := repo.Insert(ctx, e)

				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr)
				assert.Equal(t, c.constraint, pgErr.ConstraintName)
			})
		}
	})
}

func TestDutyStatusRepository_Edits(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	repo := NewDutyStatusRepository(f.db)
	ali, err := f.drivers.GetByID(ctx, f.ali)
	require.NoError(t, err)
	user := ali.Driver.UserID
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	reason := "forgot to press OFF"

	off, err := repo.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
		Status: model.DutyOffDuty, StartedAt: day.Add(18 * time.Hour), Origin: model.DutyOriginDriver, CreatedBy: &user})
	require.NoError(t, err)
	assert.Equal(t, model.RecordActive, off.RecordStatus)

	// The edit: the same instant, sleeper instead of off duty. Old out first,
	// then the new version in: the unique index only counts active records.
	require.NoError(t, repo.Inactivate(ctx, off.ID, user))
	sb, err := repo.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
		Status: model.DutySleeperBerth, StartedAt: off.StartedAt, Origin: model.DutyOriginDriver, CreatedBy: &user,
		Replaces: &off.ID, EditReason: &reason})
	require.NoError(t, err)

	t.Run("the log reads only active records", func(t *testing.T) {
		got, err := repo.At(ctx, f.ali, day.Add(20*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, sb.ID, got.ID)

		events, err := repo.ListPeriod(ctx, f.ali, day, day.Add(24*time.Hour))
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, off.ID, *events[0].Replaces)
		assert.Equal(t, reason, *events[0].EditReason)
	})

	t.Run("the old version stays, as history", func(t *testing.T) {
		old, err := repo.GetByID(ctx, off.ID)
		require.NoError(t, err)
		assert.Equal(t, model.RecordInactive, old.RecordStatus)
		assert.NotNil(t, old.InactivatedAt)
		assert.Equal(t, user, *old.InactivatedBy)
		assert.Equal(t, model.DutyOffDuty, old.Status, "unchanged: it is what the log used to say")

		history, err := repo.ListReplaced(ctx, f.ali, day, day.Add(24*time.Hour))
		require.NoError(t, err)
		require.Len(t, history, 1)
		assert.Equal(t, off.ID, history[0].ID)
	})

	t.Run("a version is replaced once", func(t *testing.T) {
		assert.ErrorIs(t, repo.Inactivate(ctx, off.ID, user), model.ErrRecordReplaced, "already inactive")

		_, err := repo.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
			Status: model.DutyOnDuty, StartedAt: day.Add(19 * time.Hour), Origin: model.DutyOriginDriver, CreatedBy: &user,
			Replaces: &off.ID, EditReason: &reason})
		assert.ErrorIs(t, err, model.ErrRecordReplaced, "a second edit of the same version would fork the history")
	})

	t.Run("an edit says why", func(t *testing.T) {
		short := "oop"
		_, err := repo.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: f.ali,
			Status: model.DutyOnDuty, StartedAt: day.Add(21 * time.Hour), Origin: model.DutyOriginDriver, EditReason: &short})
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		assert.Equal(t, "dse_edit_reason", pgErr.ConstraintName)
	})

	t.Run("certifications: the latest counts", func(t *testing.T) {
		at, err := repo.LastCertification(ctx, f.ali, "2026-11-10")
		require.NoError(t, err)
		assert.Nil(t, at, "never signed")

		first, err := repo.Certify(ctx, model.LogCertification{CompanyID: f.alpha.ID, DriverID: f.ali, Date: "2026-11-10", CertifiedBy: user})
		require.NoError(t, err)
		second, err := repo.Certify(ctx, model.LogCertification{CompanyID: f.alpha.ID, DriverID: f.ali, Date: "2026-11-10", CertifiedBy: user})
		require.NoError(t, err)
		assert.NotEqual(t, first.ID, second.ID, "append-only")

		at, err = repo.LastCertification(ctx, f.ali, "2026-11-10")
		require.NoError(t, err)
		assert.Equal(t, second.CertifiedAt, *at)

		other, err := repo.LastCertification(ctx, f.ali, "2026-11-09")
		require.NoError(t, err)
		assert.Nil(t, other, "per day")
	})
}

func TestDutyStatusRepository_ListPeriodMany(t *testing.T) {
	f := newAssignmentFixture(t)
	ctx := context.Background()
	repo := NewDutyStatusRepository(f.db)
	day := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	add := func(driver uuid.UUID, s model.DutyStatus, at time.Time) {
		t.Helper()
		_, err := repo.Insert(ctx, model.DutyStatusEvent{CompanyID: f.alpha.ID, DriverID: driver, Status: s, StartedAt: at, Origin: model.DutyOriginDriver})
		require.NoError(t, err)
	}
	add(f.ali, model.DutyOffDuty, day.Add(-10*time.Hour))
	add(f.ali, model.DutySleeperBerth, day.Add(-2*time.Hour)) // in force at the start
	add(f.ali, model.DutyOnDuty, day.Add(6*time.Hour))
	add(f.bob, model.DutyOnDuty, day.Add(8*time.Hour))
	add(f.bob, model.DutyOffDuty, day.Add(30*time.Hour)) // after the period

	got, err := repo.ListPeriodMany(ctx, []uuid.UUID{f.ali, f.bob, f.dan}, day, day.Add(24*time.Hour))

	require.NoError(t, err)
	statuses := func(es []model.DutyStatusEvent) (out []model.DutyStatus) {
		for _, e := range es {
			out = append(out, e.Status)
		}
		return out
	}
	assert.Equal(t, []model.DutyStatus{model.DutySleeperBerth, model.DutyOnDuty}, statuses(got[f.ali]),
		"each driver's own record in force at the start, not the older one")
	assert.Equal(t, []model.DutyStatus{model.DutyOnDuty}, statuses(got[f.bob]))
	assert.Empty(t, got[f.dan], "no records")

	// The same answer as one ListPeriod per driver.
	for _, d := range []uuid.UUID{f.ali, f.bob} {
		one, err := repo.ListPeriod(ctx, d, day, day.Add(24*time.Hour))
		require.NoError(t, err)
		assert.Equal(t, one, got[d])
	}
}
