package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func point(seq int64, at time.Time, speed float64) model.TelemetryPoint {
	lat, lon := 32.7767, -96.7970
	return model.TelemetryPoint{Seq: seq, RecordedAt: at, Latitude: &lat, Longitude: &lon,
		SpeedMPH: speed, OdometerMi: 120000 + float64(seq), EngineHours: 8000, EngineOn: true}
}

func TestTelemetryRepository_InsertBatch(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	repo := NewTelemetryRepository(f.pool)
	d := f.device(t, f.alpha.ID, "PT30-0001")
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("one statement stores the batch; moving comes from speed", func(t *testing.T) {
		noFix := point(3, now, 0)
		noFix.Latitude, noFix.Longitude = nil, nil

		accepted, err := repo.InsertBatch(ctx, f.alpha.ID, d.ID, []model.TelemetryPoint{
			point(1, now.Add(-2*time.Second), 61.5), point(2, now.Add(-time.Second), 4), noFix,
		})

		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{1, 2, 3}, accepted)
		events, err := repo.ListByDevice(ctx, d.ID, 10)
		require.NoError(t, err)
		require.Len(t, events, 3)
		assert.True(t, events[0].Moving, "61.5 mph")
		assert.False(t, events[1].Moving, "4 mph is not moving (threshold 5)")
		assert.Nil(t, events[2].Latitude, "no GPS fix is stored as no position")
		assert.Nil(t, events[0].TruckID, "not installed anywhere")
		assert.Equal(t, now.Add(-2*time.Second), events[0].RecordedAt.UTC())
	})

	t.Run("sending again stores nothing twice", func(t *testing.T) {
		accepted, err := repo.InsertBatch(ctx, f.alpha.ID, d.ID, []model.TelemetryPoint{
			point(2, now, 10), point(3, now, 10), point(4, now, 10),
		})

		require.NoError(t, err)
		assert.Equal(t, []int64{4}, accepted, "2 and 3 were already stored")
		events, err := repo.ListByDevice(ctx, d.ID, 10)
		require.NoError(t, err)
		assert.Len(t, events, 4)
		assert.False(t, events[1].Moving, "the first copy of seq 2 is kept, not overwritten")
	})

	t.Run("the same seq twice in one batch is stored once", func(t *testing.T) {
		accepted, err := repo.InsertBatch(ctx, f.alpha.ID, d.ID, []model.TelemetryPoint{point(5, now, 1), point(5, now, 2)})
		require.NoError(t, err)
		assert.Equal(t, []int64{5}, accepted)
	})
}

// A point buffered offline lands on the truck the device was in WHEN it was
// recorded, not the truck it is in when the point finally arrives.
func TestTelemetryRepository_TruckAtRecordedAt(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	repo := NewTelemetryRepository(f.pool)
	d := f.device(t, f.alpha.ID, "PT30-0001")

	inT1, err := f.install(d, f.alphaT1)
	require.NoError(t, err)
	_, err = f.installs.EndOpenByDevice(ctx, d.ID, nil)
	require.NoError(t, err)
	inT2, err := f.install(d, f.alphaT2)
	require.NoError(t, err)
	// Truck 1 from 08:00 to 12:00, truck 2 from 12:00 on.
	day := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	_, err = f.pool.Exec(ctx, `UPDATE eld_truck_assignments SET started_at = $2, ended_at = $3 WHERE id = $1`,
		inT1.ID, day.Add(8*time.Hour), day.Add(12*time.Hour))
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE eld_truck_assignments SET started_at = $2 WHERE id = $1`, inT2.ID, day.Add(12*time.Hour))
	require.NoError(t, err)

	// All arrive now, in one late batch.
	_, err = repo.InsertBatch(ctx, f.alpha.ID, d.ID, []model.TelemetryPoint{
		point(1, day.Add(7*time.Hour), 0),   // before any install
		point(2, day.Add(10*time.Hour), 55), // in truck 1
		point(3, day.Add(12*time.Hour), 55), // the move moment: truck 2
		point(4, day.Add(15*time.Hour), 55), // in truck 2
	})
	require.NoError(t, err)

	events, err := repo.ListByDevice(ctx, d.ID, 10)
	require.NoError(t, err)
	require.Len(t, events, 4)
	assert.Nil(t, events[0].TruckID)
	assert.Equal(t, f.alphaT1.ID, *events[1].TruckID)
	assert.Equal(t, f.alphaT2.ID, *events[2].TruckID, "[started_at, ended_at): 12:00 belongs to the new stint")
	assert.Equal(t, f.alphaT2.ID, *events[3].TruckID)
}

func TestTelemetryRepository_DatabaseRules(t *testing.T) {
	f := newELDFixture(t)
	ctx := context.Background()
	repo := NewTelemetryRepository(f.pool)
	d := f.device(t, f.alpha.ID, "PT30-0001")
	bad := point(1, time.Now(), 10)
	lat := 91.0
	bad.Latitude = &lat

	_, err := repo.InsertBatch(ctx, f.alpha.ID, d.ID, []model.TelemetryPoint{bad})
	assert.ErrorContains(t, err, "eld_events_latitude", "the service checks first; the database is the last guard")

	_, err = repo.InsertBatch(ctx, f.bravo.ID, d.ID, []model.TelemetryPoint{point(1, time.Now(), 10)})
	assert.ErrorContains(t, err, "eld_events_device_company_fkey", "a device's events belong to its company")
}
