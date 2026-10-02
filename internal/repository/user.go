package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

const userColumns = `id, company_id, email, username, password_hash, first_name, last_name,
	is_active, last_login_at, created_at, updated_at, deleted_at`

type UserRepository struct {
	db DBTX
}

func NewUserRepository(db DBTX) *UserRepository {
	return &UserRepository{db: db}
}

// Create inserts a user and assigns the given system roles in ONE transaction:
// either the user and all roles are saved, or nothing is.
// roleNames must not contain duplicates.
func (r *UserRepository) Create(ctx context.Context, u model.User, roleNames []string) (*model.User, error) {
	var created *model.User

	// BeginFunc: BEGIN → fn(tx) → COMMIT if fn returns nil, ROLLBACK otherwise
	// (also on panic). No way to forget the rollback.
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO users (company_id, email, username, password_hash, first_name, last_name)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+userColumns,
			u.CompanyID, u.Email, u.Username, u.PasswordHash, u.FirstName, u.LastName,
		)
		var err error
		if created, err = scanUser(row); err != nil {
			return err
		}

		// Insert all roles with one statement. name = ANY($2) matches any
		// element of the Go slice (sent as a Postgres array).
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, id FROM roles
			WHERE company_id IS NULL AND name = ANY($2)`,
			created.ID, roleNames,
		)
		if err != nil {
			return err
		}
		// Fewer rows than names = at least one unknown role. Returning an
		// error here rolls back the user insert too.
		if int(tag.RowsAffected()) != len(roleNames) {
			return model.ErrRoleNotFound
		}
		return nil
	})
	if err != nil {
		return nil, mapUserError("create user", err)
	}
	return created, nil
}

// GetByID returns an alive user or model.ErrNotFound.
func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	row := r.db.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE id = $1 AND deleted_at IS NULL`,
		id,
	)
	u, err := scanUser(row)
	if err != nil {
		return nil, mapUserError("get user", err)
	}
	return u, nil
}

// GetByLogin finds an alive user by email OR username, case-insensitively.
// Used by login. An email can never match a username, because usernames
// cannot contain "@".
func (r *UserRepository) GetByLogin(ctx context.Context, login string) (*model.User, error) {
	// lower(...) on both sides matches the lower(...) unique indexes, so
	// Postgres can use them for this lookup.
	row := r.db.QueryRow(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE deleted_at IS NULL
		  AND (lower(email) = lower($1) OR lower(username) = lower($1))`,
		login,
	)
	u, err := scanUser(row)
	if err != nil {
		return nil, mapUserError("get user by login", err)
	}
	return u, nil
}

// GetPermissions returns the union of the permissions of all the user's roles.
//
// A deactivated or deleted user has NO permissions. Their access token may
// still be valid for a few minutes (JWTs cannot be revoked), but every
// permission check fails immediately.
func (r *UserRepository) GetPermissions(ctx context.Context, userID uuid.UUID) ([]model.Permission, error) {
	rows, err := r.db.Query(ctx, `
		SELECT DISTINCT rp.permission_code
		FROM users u
		JOIN user_roles ur       ON ur.user_id = u.id
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		WHERE u.id = $1
		  AND u.is_active
		  AND u.deleted_at IS NULL
		ORDER BY 1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get permissions: %w", err)
	}
	perms, err := pgx.CollectRows(rows, pgx.RowTo[model.Permission])
	if err != nil {
		return nil, fmt.Errorf("scan permissions: %w", err)
	}
	return perms, nil
}

func scanUser(row pgx.Row) (*model.User, error) {
	var u model.User
	err := row.Scan(
		&u.ID, &u.CompanyID, &u.Email, &u.Username, &u.PasswordHash, &u.FirstName, &u.LastName,
		&u.IsActive, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func mapUserError(op string, err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return model.ErrNotFound
	case errors.Is(err, model.ErrRoleNotFound):
		return err
	case isUniqueViolation(err, "users_email_alive_uniq"):
		return model.ErrEmailTaken
	case isUniqueViolation(err, "users_username_alive_uniq"):
		return model.ErrUsernameTaken
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
