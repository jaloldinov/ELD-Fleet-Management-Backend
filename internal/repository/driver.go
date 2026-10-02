package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// driverDetailsSelect joins each driver with its user, so one query returns
// everything a client shows in a driver list (no N+1).
const driverDetailsSelect = `
	SELECT d.id, d.user_id, d.company_id, d.license_number, d.license_state,
	       d.license_expires_at, d.created_at, d.updated_at, d.deleted_at,
	       u.email, u.username, u.first_name, u.last_name, u.is_active, u.last_login_at
	FROM drivers d
	JOIN users u ON u.id = d.user_id`

type DriverRepository struct {
	db DBTX
}

func NewDriverRepository(db DBTX) *DriverRepository {
	return &DriverRepository{db: db}
}

// q returns the connection to use: the caller's transaction if ctx carries
// one, otherwise the pool.
func (r *DriverRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Create inserts a driver profile and returns its id. It is meant to run in
// the same transaction as the user insert (see service.DriverService).
func (r *DriverRepository) Create(ctx context.Context, d model.Driver) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.q(ctx).QueryRow(ctx, `
		INSERT INTO drivers (user_id, company_id, license_number, license_state, license_expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		d.UserID, d.CompanyID, d.LicenseNumber, d.LicenseState, d.LicenseExpiresAt,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, mapDriverError("create driver", err)
	}
	return id, nil
}

// GetByID returns an alive driver (with user fields) or model.ErrNotFound.
func (r *DriverRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.DriverDetails, error) {
	row := r.q(ctx).QueryRow(ctx, driverDetailsSelect+`
		WHERE d.id = $1 AND d.deleted_at IS NULL AND u.deleted_at IS NULL`, id)
	dd, err := scanDriverDetails(row)
	if err != nil {
		return nil, mapDriverError("get driver", err)
	}
	return dd, nil
}

// GetByUserID returns the driver profile of a user, or model.ErrNotFound.
func (r *DriverRepository) GetByUserID(ctx context.Context, userID uuid.UUID) (*model.DriverDetails, error) {
	row := r.q(ctx).QueryRow(ctx, driverDetailsSelect+`
		WHERE d.user_id = $1 AND d.deleted_at IS NULL AND u.deleted_at IS NULL`, userID)
	dd, err := scanDriverDetails(row)
	if err != nil {
		return nil, mapDriverError("get driver by user", err)
	}
	return dd, nil
}

// List returns one page of alive drivers (oldest first) and the total count.
func (r *DriverRepository) List(ctx context.Context, f model.DriverFilter, limit, offset int) ([]model.DriverDetails, int, error) {
	const where = `
		WHERE d.deleted_at IS NULL AND u.deleted_at IS NULL
		  AND ($1::uuid IS NULL OR d.company_id = $1)`

	var total int
	if err := r.q(ctx).QueryRow(ctx,
		`SELECT count(*) FROM drivers d JOIN users u ON u.id = d.user_id`+where, f.CompanyID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count drivers: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, driverDetailsSelect+where+`
		ORDER BY d.id
		LIMIT $2 OFFSET $3`,
		f.CompanyID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list drivers: %w", err)
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.DriverDetails, error) {
		dd, err := scanDriverDetails(row)
		if err != nil {
			return model.DriverDetails{}, err
		}
		return *dd, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan drivers: %w", err)
	}
	return items, total, nil
}

// UpdateLicense saves the license fields of an alive driver.
func (r *DriverRepository) UpdateLicense(ctx context.Context, d model.Driver) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE drivers
		SET license_number = $2, license_state = $3, license_expires_at = $4
		WHERE id = $1 AND deleted_at IS NULL`,
		d.ID, d.LicenseNumber, d.LicenseState, d.LicenseExpiresAt,
	)
	if err != nil {
		return mapDriverError("update driver", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

// SoftDelete marks a driver profile as deleted.
func (r *DriverRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE drivers SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete driver: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

func scanDriverDetails(row pgx.Row) (*model.DriverDetails, error) {
	var dd model.DriverDetails
	d, u := &dd.Driver, &dd.User
	err := row.Scan(
		&d.ID, &d.UserID, &d.CompanyID, &d.LicenseNumber, &d.LicenseState,
		&d.LicenseExpiresAt, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt,
		&u.Email, &u.Username, &u.FirstName, &u.LastName, &u.IsActive, &u.LastLoginAt,
	)
	if err != nil {
		return nil, err
	}
	u.ID, u.CompanyID = d.UserID, &d.CompanyID
	return &dd, nil
}

func mapDriverError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "drivers_license_alive_uniq"):
		return model.ErrLicenseTaken
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
