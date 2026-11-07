package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const eldAssignmentColumns = `id, company_id, eld_device_id, truck_id,
	started_at, ended_at, started_by, ended_by`

// ELDAssignmentRepository stores ELD ↔ truck stints, like
// DriverAssignmentRepository: only inserted and ended, never deleted.
type ELDAssignmentRepository struct {
	db DBTX
}

func NewELDAssignmentRepository(db DBTX) *ELDAssignmentRepository {
	return &ELDAssignmentRepository{db: db}
}

func (r *ELDAssignmentRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Open installs a device in a truck now. ErrTruckHasELD if the truck already
// has one (take it out first, or replace it).
func (r *ELDAssignmentRepository) Open(ctx context.Context, a model.ELDAssignment) (*model.ELDAssignment, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO eld_truck_assignments (company_id, eld_device_id, truck_id, started_by)
		VALUES ($1, $2, $3, $4)
		RETURNING `+eldAssignmentColumns,
		a.CompanyID, a.ELDDeviceID, a.TruckID, a.StartedBy,
	)
	created, err := scanELDAssignment(row)
	if err != nil {
		return nil, mapELDAssignmentError("open eld assignment", err)
	}
	return created, nil
}

// GetOpenByDevice returns where the device is installed now, or ErrNotFound.
func (r *ELDAssignmentRepository) GetOpenByDevice(ctx context.Context, deviceID uuid.UUID) (*model.ELDAssignment, error) {
	return r.one(ctx, "get open eld assignment", `
		SELECT `+eldAssignmentColumns+` FROM eld_truck_assignments
		WHERE eld_device_id = $1 AND ended_at IS NULL`, deviceID)
}

// GetOpenByTruck returns the device installed in the truck now, or
// ErrNotFound.
func (r *ELDAssignmentRepository) GetOpenByTruck(ctx context.Context, truckID uuid.UUID) (*model.ELDAssignment, error) {
	return r.one(ctx, "get truck's open eld assignment", `
		SELECT `+eldAssignmentColumns+` FROM eld_truck_assignments
		WHERE truck_id = $1 AND ended_at IS NULL`, truckID)
}

// GetInstalled returns the device in the truck now, with its serial
// number, or ErrNotFound if the truck has none.
func (r *ELDAssignmentRepository) GetInstalled(ctx context.Context, truckID uuid.UUID) (*model.InstalledDevice, error) {
	var d model.InstalledDevice
	a := &d.Assignment
	err := r.q(ctx).QueryRow(ctx, `
		SELECT a.id, a.company_id, a.eld_device_id, a.truck_id,
		       a.started_at, a.ended_at, a.started_by, a.ended_by, e.serial_number
		FROM eld_truck_assignments a
		JOIN eld_devices e ON e.id = a.eld_device_id
		WHERE a.truck_id = $1 AND a.ended_at IS NULL`, truckID,
	).Scan(&a.ID, &a.CompanyID, &a.ELDDeviceID, &a.TruckID,
		&a.StartedAt, &a.EndedAt, &a.StartedBy, &a.EndedBy, &d.SerialNumber)
	if err != nil {
		return nil, mapELDAssignmentError("get installed device", err)
	}
	return &d, nil
}

// EndOpenByDevice takes the device out of its truck and returns that stint,
// or ErrNotFound if it was not installed.
func (r *ELDAssignmentRepository) EndOpenByDevice(ctx context.Context, deviceID uuid.UUID, endedBy *uuid.UUID) (*model.ELDAssignment, error) {
	return r.one(ctx, "end eld assignment", `
		UPDATE eld_truck_assignments SET ended_at = now(), ended_by = $2
		WHERE eld_device_id = $1 AND ended_at IS NULL
		RETURNING `+eldAssignmentColumns, deviceID, endedBy)
}

// EndOpenByTruck takes whatever device is in the truck out of it and returns
// that stint, or ErrNotFound if the truck had none.
func (r *ELDAssignmentRepository) EndOpenByTruck(ctx context.Context, truckID uuid.UUID, endedBy *uuid.UUID) (*model.ELDAssignment, error) {
	return r.one(ctx, "end truck's eld assignment", `
		UPDATE eld_truck_assignments SET ended_at = now(), ended_by = $2
		WHERE truck_id = $1 AND ended_at IS NULL
		RETURNING `+eldAssignmentColumns, truckID, endedBy)
}

// ListHistory returns one page of device stints, newest first, and the
// total. Deleted devices and trucks are included: history outlives them.
func (r *ELDAssignmentRepository) ListHistory(ctx context.Context, f model.ELDAssignmentFilter, limit, offset int) ([]model.ELDAssignmentRecord, int, error) {
	from := `
		FROM eld_truck_assignments a
		JOIN trucks t ON t.id = a.truck_id
		JOIN eld_devices e ON e.id = a.eld_device_id
		WHERE ($1::uuid IS NULL OR a.company_id = $1)
		  AND ($2::uuid IS NULL OR a.eld_device_id = $2)
		  AND ($3::uuid IS NULL OR a.truck_id = $3)` + stintPeriodSQL("a", "$4", "$5", "$6")
	args := append([]any{f.CompanyID, f.ELDDeviceID, f.TruckID}, periodArgs(f.Period)...)

	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count eld assignments: %w", err)
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT a.id, a.company_id, a.eld_device_id, a.truck_id,
		       a.started_at, a.ended_at, a.started_by, a.ended_by,
		       t.unit_number, e.serial_number`+from+`
		ORDER BY a.started_at DESC, a.id DESC
		LIMIT $7 OFFSET $8`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list eld assignments: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.ELDAssignmentRecord, error) {
		var rec model.ELDAssignmentRecord
		a := &rec.Assignment
		err := row.Scan(&a.ID, &a.CompanyID, &a.ELDDeviceID, &a.TruckID,
			&a.StartedAt, &a.EndedAt, &a.StartedBy, &a.EndedBy, &rec.TruckUnitNumber, &rec.SerialNumber)
		return rec, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan eld assignments: %w", err)
	}
	return list, total, nil
}

func (r *ELDAssignmentRepository) one(ctx context.Context, op, sql string, args ...any) (*model.ELDAssignment, error) {
	a, err := scanELDAssignment(r.q(ctx).QueryRow(ctx, sql, args...))
	if err != nil {
		return nil, mapELDAssignmentError(op, err)
	}
	return a, nil
}

func scanELDAssignment(row pgx.Row) (*model.ELDAssignment, error) {
	var a model.ELDAssignment
	err := row.Scan(&a.ID, &a.CompanyID, &a.ELDDeviceID, &a.TruckID,
		&a.StartedAt, &a.EndedAt, &a.StartedBy, &a.EndedBy)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func mapELDAssignmentError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "eta_truck_open_uniq"):
		return model.ErrTruckHasELD
	default:
		// eta_device_open_uniq lands here: the service locks the device and
		// ends its stint first, so a second open stint means a bug. Same for
		// the company foreign keys.
		return fmt.Errorf("%s: %w", op, err)
	}
}
