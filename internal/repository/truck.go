package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const truckColumns = `id, company_id, unit_number, vin, make, model, year,
	license_plate, plate_state, is_active, created_at, updated_at, deleted_at`

type TruckRepository struct {
	db DBTX
}

func NewTruckRepository(db DBTX) *TruckRepository {
	return &TruckRepository{db: db}
}

// q returns the connection to use: the caller's transaction if ctx carries
// one, otherwise the pool.
func (r *TruckRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

func (r *TruckRepository) Create(ctx context.Context, t model.Truck) (*model.Truck, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO trucks (company_id, unit_number, vin, make, model, year, license_plate, plate_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+truckColumns,
		t.CompanyID, t.UnitNumber, t.VIN, t.Make, t.Model, t.Year, t.LicensePlate, t.PlateState,
	)
	created, err := scanTruck(row)
	if err != nil {
		return nil, mapTruckError("create truck", err)
	}
	return created, nil
}

func (r *TruckRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Truck, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+truckColumns+` FROM trucks
		WHERE id = $1 AND deleted_at IS NULL`, id)
	t, err := scanTruck(row)
	if err != nil {
		return nil, mapTruckError("get truck", err)
	}
	return t, nil
}

// LockByID reads an alive truck and locks its row until the transaction ends
// (ctx must carry one). Use it before deciding something from the truck's
// state, e.g. "it is active, so a driver may be assigned": a concurrent
// deactivation or deletion waits for our COMMIT instead of slipping in
// between our check and our write. A request that waited here reads the
// row as it is after the other COMMIT, so it sees the new state.
func (r *TruckRepository) LockByID(ctx context.Context, id uuid.UUID) (*model.Truck, error) {
	if err := requireTx(ctx); err != nil {
		return nil, err
	}
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+truckColumns+` FROM trucks
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`, id)
	t, err := scanTruck(row)
	if err != nil {
		return nil, mapTruckError("lock truck", err)
	}
	return t, nil
}

func (r *TruckRepository) List(ctx context.Context, f model.TruckFilter, limit, offset int) ([]model.Truck, int, error) {
	const where = `
		WHERE deleted_at IS NULL
		  AND ($1::uuid IS NULL OR company_id = $1)
		  AND ($2::boolean IS NULL OR is_active = $2)`

	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM trucks`+where,
		f.CompanyID, f.IsActive).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count trucks: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, `SELECT `+truckColumns+` FROM trucks`+where+`
		ORDER BY id
		LIMIT $3 OFFSET $4`,
		f.CompanyID, f.IsActive, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list trucks: %w", err)
	}
	trucks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Truck, error) {
		t, err := scanTruck(row)
		if err != nil {
			return model.Truck{}, err
		}
		return *t, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan trucks: %w", err)
	}
	return trucks, total, nil
}

// Update saves every editable field. The VIN is not editable: it is the
// truck's identity.
func (r *TruckRepository) Update(ctx context.Context, t model.Truck) (*model.Truck, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE trucks
		SET unit_number = $2, make = $3, model = $4, year = $5,
		    license_plate = $6, plate_state = $7, is_active = $8
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+truckColumns,
		t.ID, t.UnitNumber, t.Make, t.Model, t.Year, t.LicensePlate, t.PlateState, t.IsActive,
	)
	updated, err := scanTruck(row)
	if err != nil {
		return nil, mapTruckError("update truck", err)
	}
	return updated, nil
}

// SoftDelete marks the truck deleted. Its drivers and device are released
// first, by the service, in the same transaction (see
// AssignmentService.ReleaseTruck for why not here, in one statement).
func (r *TruckRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE trucks SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete truck: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

func scanTruck(row pgx.Row) (*model.Truck, error) {
	var t model.Truck
	var year *int16 // SMALLINT in Postgres
	err := row.Scan(&t.ID, &t.CompanyID, &t.UnitNumber, &t.VIN, &t.Make, &t.Model, &year,
		&t.LicensePlate, &t.PlateState, &t.IsActive, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if err != nil {
		return nil, err
	}
	if year != nil {
		y := int(*year)
		t.Year = &y
	}
	return &t, nil
}

func mapTruckError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "trucks_vin_alive_uniq"):
		return model.ErrVINTaken
	case isUniqueViolation(err, "trucks_unit_number_alive_uniq"):
		return model.ErrUnitNumberTaken
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}

// CountActive returns how many active trucks the company has: the quantity
// of its per-truck subscription items.
func (r *TruckRepository) CountActive(ctx context.Context, companyID uuid.UUID) (int, error) {
	var n int
	err := r.q(ctx).QueryRow(ctx, `
		SELECT count(*) FROM trucks
		WHERE company_id = $1 AND is_active AND deleted_at IS NULL`, companyID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count active trucks: %w", err)
	}
	return n, nil
}
