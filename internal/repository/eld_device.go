package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// The truck comes from the device's open stint (at most one, thanks to
// eta_device_open_uniq; the subquery uses that index). As a column
// expression it also works in RETURNING, so every query returns the same
// shape.
const eldDeviceColumns = `id, company_id, serial_number, model, firmware_version,
	(SELECT a.truck_id FROM eld_truck_assignments a
	 WHERE a.eld_device_id = eld_devices.id AND a.ended_at IS NULL) AS truck_id,
	status, is_active, key_created_at, key_last_used_at, created_at, updated_at, deleted_at`

type ELDDeviceRepository struct {
	db DBTX
}

func NewELDDeviceRepository(db DBTX) *ELDDeviceRepository {
	return &ELDDeviceRepository{db: db}
}

// q returns the connection to use: the caller's transaction if ctx carries
// one, otherwise the pool.
func (r *ELDDeviceRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Create inserts a device that is not installed anywhere yet.
func (r *ELDDeviceRepository) Create(ctx context.Context, d model.ELDDevice) (*model.ELDDevice, error) {
	row := r.q(ctx).QueryRow(ctx, `
		INSERT INTO eld_devices (company_id, serial_number, model, firmware_version)
		VALUES ($1, $2, $3, $4)
		RETURNING `+eldDeviceColumns,
		d.CompanyID, d.SerialNumber, d.Model, d.FirmwareVersion,
	)
	created, err := scanELDDevice(row)
	if err != nil {
		return nil, mapELDDeviceError("create eld device", err)
	}
	return created, nil
}

func (r *ELDDeviceRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+eldDeviceColumns+` FROM eld_devices
		WHERE id = $1 AND deleted_at IS NULL`, id)
	d, err := scanELDDevice(row)
	if err != nil {
		return nil, mapELDDeviceError("get eld device", err)
	}
	return d, nil
}

func (r *ELDDeviceRepository) List(ctx context.Context, f model.ELDDeviceFilter, limit, offset int) ([]model.ELDDevice, int, error) {
	const where = `
		WHERE deleted_at IS NULL
		  AND ($1::uuid IS NULL OR company_id = $1)
		  AND ($2::boolean IS NULL OR is_active = $2)
		  AND ($3::uuid IS NULL OR EXISTS (
		      SELECT 1 FROM eld_truck_assignments a
		      WHERE a.eld_device_id = eld_devices.id AND a.ended_at IS NULL AND a.truck_id = $3))
		  AND ($4::text IS NULL OR status = $4)`

	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM eld_devices`+where,
		f.CompanyID, f.IsActive, f.TruckID, f.Status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count eld devices: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, `SELECT `+eldDeviceColumns+` FROM eld_devices`+where+`
		ORDER BY id
		LIMIT $5 OFFSET $6`,
		f.CompanyID, f.IsActive, f.TruckID, f.Status, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list eld devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.ELDDevice, error) {
		d, err := scanELDDevice(row)
		if err != nil {
			return model.ELDDevice{}, err
		}
		return *d, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan eld devices: %w", err)
	}
	return devices, total, nil
}

// Update saves the editable fields. The serial number (identity) and the
// truck (an ELD stint, see ELDAssignmentRepository) are not changed here.
func (r *ELDDeviceRepository) Update(ctx context.Context, d model.ELDDevice) (*model.ELDDevice, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE eld_devices
		SET model = $2, firmware_version = $3, status = $4
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+eldDeviceColumns,
		d.ID, d.Model, d.FirmwareVersion, d.Status,
	)
	updated, err := scanELDDevice(row)
	if err != nil {
		return nil, mapELDDeviceError("update eld device", err)
	}
	return updated, nil
}

// LockByID reads an alive device and locks its row until the transaction
// ends (ctx must carry one); see TruckRepository.LockByID. Its TruckID is
// read as the statement started: after waiting for the lock, ask
// ELDAssignmentRepository.GetOpenByDevice for the current truck.
func (r *ELDDeviceRepository) LockByID(ctx context.Context, id uuid.UUID) (*model.ELDDevice, error) {
	if err := requireTx(ctx); err != nil {
		return nil, err
	}
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+eldDeviceColumns+` FROM eld_devices
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`, id)
	d, err := scanELDDevice(row)
	if err != nil {
		return nil, mapELDDeviceError("lock eld device", err)
	}
	return d, nil
}

// SetKey stores the hash of the device's new API key (replacing, and so
// revoking, the old one), or with hash nil removes the key.
func (r *ELDDeviceRepository) SetKey(ctx context.Context, id uuid.UUID, hash []byte) (*model.ELDDevice, error) {
	row := r.q(ctx).QueryRow(ctx, `
		UPDATE eld_devices
		SET key_hash = $2,
		    key_created_at = CASE WHEN $2::bytea IS NULL THEN NULL ELSE now() END,
		    key_last_used_at = NULL
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+eldDeviceColumns, id, hash)
	d, err := scanELDDevice(row)
	if err != nil {
		return nil, mapELDDeviceError("set device key", err)
	}
	return d, nil
}

// GetByKeyHash finds the alive device whose key hashes to hash, or
// ErrNotFound. Runs on every device request: eld_devices_key_hash_uniq.
func (r *ELDDeviceRepository) GetByKeyHash(ctx context.Context, hash []byte) (*model.ELDDevice, error) {
	row := r.q(ctx).QueryRow(ctx, `
		SELECT `+eldDeviceColumns+` FROM eld_devices
		WHERE key_hash = $1 AND deleted_at IS NULL`, hash)
	d, err := scanELDDevice(row)
	if err != nil {
		return nil, mapELDDeviceError("get device by key", err)
	}
	return d, nil
}

// TouchKey records that the device's key was just used, at most once a
// minute: a device calls every few seconds, and one row write per call
// would be wasted work (the minute is precise enough for "last seen").
func (r *ELDDeviceRepository) TouchKey(ctx context.Context, id uuid.UUID) error {
	_, err := r.q(ctx).Exec(ctx, `
		UPDATE eld_devices SET key_last_used_at = now()
		WHERE id = $1
		  AND (key_last_used_at IS NULL OR key_last_used_at < now() - interval '1 minute')`, id)
	if err != nil {
		return fmt.Errorf("touch device key: %w", err)
	}
	return nil
}

// SoftDelete marks the device deleted and removes its key (a deleted
// device can never call the API). Take it out of its truck first
// (AssignmentService.ReleaseDevice), in the same transaction.
func (r *ELDDeviceRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE eld_devices
		SET deleted_at = now(), key_hash = NULL, key_created_at = NULL
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete eld device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

func scanELDDevice(row pgx.Row) (*model.ELDDevice, error) {
	var d model.ELDDevice
	err := row.Scan(&d.ID, &d.CompanyID, &d.SerialNumber, &d.Model, &d.FirmwareVersion,
		&d.TruckID, &d.Status, &d.IsActive, &d.KeyCreatedAt, &d.KeyLastUsedAt, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func mapELDDeviceError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case isUniqueViolation(err, "eld_devices_serial_alive_uniq"):
		return model.ErrSerialTaken
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
