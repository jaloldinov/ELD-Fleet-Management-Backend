package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// TelemetryRepository stores what ELD devices report (eld_events).
type TelemetryRepository struct {
	db DBTX
}

func NewTelemetryRepository(db DBTX) *TelemetryRepository {
	return &TelemetryRepository{db: db}
}

func (r *TelemetryRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// InsertBatch stores a device's points in ONE statement and returns the seqs
// that were new. Points whose seq is already stored (a batch sent again
// after a lost reply, or offline points sent twice) are skipped silently:
// ON CONFLICT DO NOTHING on the (device, seq) unique index.
//
// One statement for the whole batch: unnest turns the parallel arrays
// (all seqs, all times, ...) back into rows, so 500 points cost one round
// trip to the database instead of 500.
//
// Each point gets the truck the device was installed in at ITS recorded_at,
// read from the stint history (the same [started_at, ended_at) rule as the
// history lists), not the truck it is in now.
func (r *TelemetryRepository) InsertBatch(ctx context.Context, companyID, deviceID uuid.UUID, points []model.TelemetryPoint) ([]int64, error) {
	n := len(points)
	var (
		seqs     = make([]int64, n)
		times    = make([]time.Time, n)
		lats     = make([]*float64, n)
		lons     = make([]*float64, n)
		speeds   = make([]float64, n)
		odos     = make([]float64, n)
		hours    = make([]float64, n)
		engineOn = make([]bool, n)
		moving   = make([]bool, n)
	)
	for i, p := range points {
		seqs[i], times[i], lats[i], lons[i] = p.Seq, p.RecordedAt, p.Latitude, p.Longitude
		speeds[i], odos[i], hours[i] = p.SpeedMPH, p.OdometerMi, p.EngineHours
		engineOn[i], moving[i] = p.EngineOn, p.Moving()
	}

	rows, err := r.q(ctx).Query(ctx, `
		INSERT INTO eld_events (company_id, eld_device_id, seq, recorded_at, truck_id,
		                        latitude, longitude, speed_mph, odometer_mi, engine_hours, engine_on, moving)
		SELECT $1, $2, p.seq, p.recorded_at,
		       (SELECT a.truck_id FROM eld_truck_assignments a
		        WHERE a.eld_device_id = $2
		          AND a.started_at <= p.recorded_at
		          AND (a.ended_at IS NULL OR a.ended_at > p.recorded_at)
		        ORDER BY a.started_at DESC
		        LIMIT 1),
		       p.lat, p.lon, p.speed, p.odo, p.hours, p.engine_on, p.moving
		FROM unnest($3::bigint[], $4::timestamptz[], $5::float8[], $6::float8[],
		            $7::float8[], $8::float8[], $9::float8[], $10::bool[], $11::bool[])
		     AS p(seq, recorded_at, lat, lon, speed, odo, hours, engine_on, moving)
		ON CONFLICT (eld_device_id, seq) DO NOTHING
		RETURNING seq`,
		companyID, deviceID, seqs, times, lats, lons, speeds, odos, hours, engineOn, moving)
	if err != nil {
		return nil, fmt.Errorf("insert eld events: %w", err)
	}
	accepted, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("insert eld events: %w", err)
	}
	return accepted, nil
}

// ListByDevice returns a device's stored events in seq order (newest
// last); a small read used by tests and, later, by the office views.
func (r *TelemetryRepository) ListByDevice(ctx context.Context, deviceID uuid.UUID, limit int) ([]model.ELDEvent, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT id, company_id, eld_device_id, seq, recorded_at, received_at, truck_id,
		       latitude, longitude, speed_mph, odometer_mi, engine_hours, engine_on, moving
		FROM eld_events
		WHERE eld_device_id = $1
		ORDER BY seq
		LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list eld events: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.ELDEvent, error) {
		var e model.ELDEvent
		err := row.Scan(&e.ID, &e.CompanyID, &e.ELDDeviceID, &e.Seq, &e.RecordedAt, &e.ReceivedAt, &e.TruckID,
			&e.Latitude, &e.Longitude, &e.SpeedMPH, &e.OdometerMi, &e.EngineHours, &e.EngineOn, &e.Moving)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan eld events: %w", err)
	}
	return events, nil
}
