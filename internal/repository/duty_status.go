package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// DutyStatusRepository stores drivers' logbooks (duty_status_events).
type DutyStatusRepository struct {
	db DBTX
}

func NewDutyStatusRepository(db DBTX) *DutyStatusRepository {
	return &DutyStatusRepository{db: db}
}

func (r *DutyStatusRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

const dutyStatusColumns = `id, company_id, driver_id, status, special, started_at, origin,
	truck_id, latitude, longitude, odometer_mi, engine_hours, note, created_by, created_at`

// Insert stores one status change. It fails with ErrDutyStatusTaken if the
// driver already has an event starting at exactly that moment.
func (r *DutyStatusRepository) Insert(ctx context.Context, e model.DutyStatusEvent) (*model.DutyStatusEvent, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO duty_status_events (company_id, driver_id, status, special, started_at, origin,
		                                truck_id, latitude, longitude, odometer_mi, engine_hours, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+dutyStatusColumns,
		e.CompanyID, e.DriverID, e.Status, e.Special, e.StartedAt, e.Origin,
		e.TruckID, e.Latitude, e.Longitude, e.OdometerMi, e.EngineHours, e.Note, e.CreatedBy)
	created, err := scanDutyStatus(row)
	if err != nil {
		return nil, mapDutyStatusError("insert duty status", err)
	}
	return created, nil
}

// At returns the event in force at an instant: the driver's last event that
// started at or before it. ErrNotFound if the driver had no event yet.
func (r *DutyStatusRepository) At(ctx context.Context, driverID uuid.UUID, at time.Time) (*model.DutyStatusEvent, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+dutyStatusColumns+`
		FROM duty_status_events
		WHERE driver_id = $1 AND started_at <= $2
		ORDER BY started_at DESC
		LIMIT 1`, driverID, at)
	e, err := scanDutyStatus(row)
	if err != nil {
		return nil, mapDutyStatusError("get duty status", err)
	}
	return e, nil
}

// ListPeriod returns the events that cover [from, to), oldest first: the
// events that started inside it, PLUS the one already in force at from.
//
// That extra event matters: a driver who went off duty yesterday at 20:00
// and changed nothing until 06:00 is off duty from 00:00 today, but no
// event of today says so. Without it the day would start with a gap.
func (r *DutyStatusRepository) ListPeriod(ctx context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error) {
	rows, err := r.q(ctx).Query(ctx, `
		(SELECT `+dutyStatusColumns+`
		 FROM duty_status_events
		 WHERE driver_id = $1 AND started_at <= $2
		 ORDER BY started_at DESC
		 LIMIT 1)
		UNION ALL
		(SELECT `+dutyStatusColumns+`
		 FROM duty_status_events
		 WHERE driver_id = $1 AND started_at > $2 AND started_at < $3)
		ORDER BY started_at`, driverID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list duty statuses: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.DutyStatusEvent, error) {
		e, err := scanDutyStatus(row)
		if err != nil {
			return model.DutyStatusEvent{}, err
		}
		return *e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan duty statuses: %w", err)
	}
	return events, nil
}

func scanDutyStatus(row pgx.Row) (*model.DutyStatusEvent, error) {
	var e model.DutyStatusEvent
	err := row.Scan(&e.ID, &e.CompanyID, &e.DriverID, &e.Status, &e.Special, &e.StartedAt, &e.Origin,
		&e.TruckID, &e.Latitude, &e.Longitude, &e.OdometerMi, &e.EngineHours, &e.Note, &e.CreatedBy, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func mapDutyStatusError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "dse_driver_started_uniq"):
		return model.ErrDutyStatusTaken
	default:
		// CHECK and company foreign keys land here: the service validates
		// first, so reaching them means a bug.
		return fmt.Errorf("%s: %w", op, err)
	}
}
