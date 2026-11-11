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
	truck_id, latitude, longitude, odometer_mi, engine_hours, note, created_by, created_at,
	record_status, replaces, edit_reason, inactivated_at, inactivated_by`

// Insert stores one active record. It fails with ErrDutyStatusTaken if the
// driver already has an active record starting at exactly that moment,
// or ErrRecordReplaced if e.Replaces was already replaced by another edit.
func (r *DutyStatusRepository) Insert(ctx context.Context, e model.DutyStatusEvent) (*model.DutyStatusEvent, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO duty_status_events (company_id, driver_id, status, special, started_at, origin,
		                                truck_id, latitude, longitude, odometer_mi, engine_hours, note, created_by,
		                                replaces, edit_reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING `+dutyStatusColumns,
		e.CompanyID, e.DriverID, e.Status, e.Special, e.StartedAt, e.Origin,
		e.TruckID, e.Latitude, e.Longitude, e.OdometerMi, e.EngineHours, e.Note, e.CreatedBy,
		e.Replaces, e.EditReason)
	created, err := scanDutyStatus(row)
	if err != nil {
		return nil, mapDutyStatusError("insert duty status", err)
	}
	return created, nil
}

// At returns the record in force at an instant: the driver's last ACTIVE
// record that started at or before it. ErrNotFound if there is none.
// (Every read that builds the log reads active records only.)
func (r *DutyStatusRepository) At(ctx context.Context, driverID uuid.UUID, at time.Time) (*model.DutyStatusEvent, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+dutyStatusColumns+`
		FROM duty_status_events
		WHERE driver_id = $1 AND started_at <= $2 AND record_status = 'active'
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
		 WHERE driver_id = $1 AND started_at <= $2 AND record_status = 'active'
		 ORDER BY started_at DESC
		 LIMIT 1)
		UNION ALL
		(SELECT `+dutyStatusColumns+`
		 FROM duty_status_events
		 WHERE driver_id = $1 AND started_at > $2 AND started_at < $3 AND record_status = 'active')
		ORDER BY started_at`, driverID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list duty statuses: %w", err)
	}
	return collectDutyStatuses(rows)
}

// ListReplaced returns the driver's inactive records (replaced by edits)
// that started in [from, to), oldest first: the day's edit history.
func (r *DutyStatusRepository) ListReplaced(ctx context.Context, driverID uuid.UUID, from, to time.Time) ([]model.DutyStatusEvent, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+dutyStatusColumns+`
		FROM duty_status_events
		WHERE driver_id = $1 AND started_at >= $2 AND started_at < $3 AND record_status = 'inactive'
		ORDER BY started_at, inactivated_at`, driverID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list replaced duty statuses: %w", err)
	}
	return collectDutyStatuses(rows)
}

// GetByID returns any record, active or not.
func (r *DutyStatusRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.DutyStatusEvent, error) {
	row := r.q(ctx).QueryRow(ctx, `SELECT `+dutyStatusColumns+` FROM duty_status_events WHERE id = $1`, id)
	e, err := scanDutyStatus(row)
	if err != nil {
		return nil, mapDutyStatusError("get duty status", err)
	}
	return e, nil
}

// Inactivate takes an active record out of the log (an edit replaces it).
// ErrRecordReplaced if it is not active (any more).
func (r *DutyStatusRepository) Inactivate(ctx context.Context, id uuid.UUID, by uuid.UUID) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE duty_status_events
		SET record_status = 'inactive', inactivated_at = now(), inactivated_by = $2
		WHERE id = $1 AND record_status = 'active'`, id, by)
	if err != nil {
		return fmt.Errorf("inactivate duty status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrRecordReplaced
	}
	return nil
}

// Certify stores the driver's signature of a day.
func (r *DutyStatusRepository) Certify(ctx context.Context, c model.LogCertification) (*model.LogCertification, error) {
	err := r.q(ctx).QueryRow(ctx, `
		INSERT INTO duty_log_certifications (company_id, driver_id, log_date, certified_by)
		VALUES ($1, $2, $3, $4)
		RETURNING id, certified_at`, c.CompanyID, c.DriverID, c.Date, c.CertifiedBy,
	).Scan(&c.ID, &c.CertifiedAt)
	if err != nil {
		return nil, fmt.Errorf("certify log: %w", err)
	}
	return &c, nil
}

// LastCertification returns when the driver last signed a day, or nil.
func (r *DutyStatusRepository) LastCertification(ctx context.Context, driverID uuid.UUID, date string) (*time.Time, error) {
	var at *time.Time
	err := r.q(ctx).QueryRow(ctx, `
		SELECT max(certified_at) FROM duty_log_certifications
		WHERE driver_id = $1 AND log_date = $2`, driverID, date).Scan(&at)
	if err != nil {
		return nil, fmt.Errorf("last certification: %w", err)
	}
	return at, nil
}

func collectDutyStatuses(rows pgx.Rows) ([]model.DutyStatusEvent, error) {
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
		&e.TruckID, &e.Latitude, &e.Longitude, &e.OdometerMi, &e.EngineHours, &e.Note, &e.CreatedBy, &e.CreatedAt,
		&e.RecordStatus, &e.Replaces, &e.EditReason, &e.InactivatedAt, &e.InactivatedBy)
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
	case isUniqueViolation(err, "dse_replaces_uniq"):
		return model.ErrRecordReplaced
	default:
		// CHECK and company foreign keys land here: the service validates
		// first, so reaching them means a bug.
		return fmt.Errorf("%s: %w", op, err)
	}
}
