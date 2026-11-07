package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const driverAssignmentColumns = `id, company_id, driver_id, truck_id, role,
	started_at, ended_at, started_by, ended_by`

// DriverAssignmentRepository stores driver ↔ truck stints. Rows are only
// inserted (Open) and ended (End*); never edited otherwise, never deleted.
//
// Business rules (is the truck active? may this user assign?) belong to the
// service, which also locks the driver and truck rows first (LockByID) so
// those rules cannot change under it before COMMIT.
type DriverAssignmentRepository struct {
	db DBTX
}

func NewDriverAssignmentRepository(db DBTX) *DriverAssignmentRepository {
	return &DriverAssignmentRepository{db: db}
}

// q returns the connection to use: the caller's transaction if ctx carries
// one, otherwise the pool.
func (r *DriverAssignmentRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Open starts a stint now. It fails with ErrDriverOnTruck if the driver
// already has an open stint (end it first to move them), or ErrSeatTaken if
// the truck already has an open stint in that role.
func (r *DriverAssignmentRepository) Open(ctx context.Context, a model.DriverAssignment) (*model.DriverAssignment, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO driver_truck_assignments (company_id, driver_id, truck_id, role, started_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+driverAssignmentColumns,
		a.CompanyID, a.DriverID, a.TruckID, a.Role, a.StartedBy,
	)
	created, err := scanDriverAssignment(row)
	if err != nil {
		return nil, mapDriverAssignmentError("open driver assignment", err)
	}
	return created, nil
}

// GetOpenByDriver returns the driver's current stint, or ErrNotFound if the
// driver is not on any truck.
func (r *DriverAssignmentRepository) GetOpenByDriver(ctx context.Context, driverID uuid.UUID) (*model.DriverAssignment, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+driverAssignmentColumns+` FROM driver_truck_assignments
		WHERE driver_id = $1 AND ended_at IS NULL`, driverID)
	a, err := scanDriverAssignment(row)
	if err != nil {
		return nil, mapDriverAssignmentError("get open driver assignment", err)
	}
	return a, nil
}

// ListOpenByTruck returns who is in the truck now: at most two stints,
// primary first. An empty slice means nobody.
func (r *DriverAssignmentRepository) ListOpenByTruck(ctx context.Context, truckID uuid.UUID) ([]model.DriverAssignment, error) {
	// 'primary' < 'co_driver' is false alphabetically, so sort explicitly.
	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+driverAssignmentColumns+` FROM driver_truck_assignments
		WHERE truck_id = $1 AND ended_at IS NULL
		ORDER BY role = 'primary' DESC`, truckID)
	if err != nil {
		return nil, fmt.Errorf("list open truck assignments: %w", err)
	}
	return collectDriverAssignments(rows)
}

// EndOpenByDriver ends the driver's current stint and returns it, or
// ErrNotFound if there was none. endedBy nil means the system ended it.
func (r *DriverAssignmentRepository) EndOpenByDriver(ctx context.Context, driverID uuid.UUID, endedBy *uuid.UUID) (*model.DriverAssignment, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE driver_truck_assignments SET ended_at = now(), ended_by = $2
		WHERE driver_id = $1 AND ended_at IS NULL
		RETURNING `+driverAssignmentColumns, driverID, endedBy)
	a, err := scanDriverAssignment(row)
	if err != nil {
		return nil, mapDriverAssignmentError("end driver assignment", err)
	}
	return a, nil
}

// EndOpenByTruck ends every current stint on the truck (it is being deleted
// or taken out of service) and returns them. None open is not an error.
func (r *DriverAssignmentRepository) EndOpenByTruck(ctx context.Context, truckID uuid.UUID, endedBy *uuid.UUID) ([]model.DriverAssignment, error) {
	rows, err := r.q(ctx).Query(ctx, `
		UPDATE driver_truck_assignments SET ended_at = now(), ended_by = $2
		WHERE truck_id = $1 AND ended_at IS NULL
		RETURNING `+driverAssignmentColumns, truckID, endedBy)
	if err != nil {
		return nil, fmt.Errorf("end truck assignments: %w", err)
	}
	return collectDriverAssignments(rows)
}

func collectDriverAssignments(rows pgx.Rows) ([]model.DriverAssignment, error) {
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.DriverAssignment, error) {
		a, err := scanDriverAssignment(row)
		if err != nil {
			return model.DriverAssignment{}, err
		}
		return *a, nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan driver assignments: %w", err)
	}
	return list, nil
}

func scanDriverAssignment(row pgx.Row) (*model.DriverAssignment, error) {
	var a model.DriverAssignment
	err := row.Scan(&a.ID, &a.CompanyID, &a.DriverID, &a.TruckID, &a.Role,
		&a.StartedAt, &a.EndedAt, &a.StartedBy, &a.EndedBy)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func mapDriverAssignmentError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "dta_driver_open_uniq"):
		return model.ErrDriverOnTruck
	case isUniqueViolation(err, "dta_truck_role_open_uniq"):
		return model.ErrSeatTaken
	default:
		// The company foreign keys land here: the service checks the
		// driver's and truck's company first, so reaching them means a bug.
		return fmt.Errorf("%s: %w", op, err)
	}
}
