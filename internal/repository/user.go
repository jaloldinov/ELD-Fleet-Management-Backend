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

// q returns the connection to use: the caller's transaction if ctx carries
// one, otherwise the pool.
func (r *UserRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// Create inserts a user and assigns the given system roles in ONE transaction:
// either the user and all roles are saved, or nothing is.
// roleNames must not contain duplicates.
func (r *UserRepository) Create(ctx context.Context, u model.User, roleNames []string) (*model.User, error) {
	var created *model.User

	// BeginFunc: BEGIN → fn(tx) → COMMIT if fn returns nil, ROLLBACK otherwise
	// (also on panic). No way to forget the rollback.
	err := pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
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
	row := r.q(ctx).QueryRow(ctx, `
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
	row := r.q(ctx).QueryRow(ctx, `
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
	rows, err := r.q(ctx).Query(ctx, `
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

// List returns one page of alive users (oldest first) and the total count.
func (r *UserRepository) List(ctx context.Context, f model.UserFilter, limit, offset int) ([]model.User, int, error) {
	// Same scope pattern as companies: the filter is applied in SQL, so other
	// tenants' rows never leave the database.
	var total int
	if err := r.q(ctx).QueryRow(ctx, `
		SELECT count(*) FROM users
		WHERE deleted_at IS NULL AND ($1::uuid IS NULL OR company_id = $1)`,
		f.CompanyID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE deleted_at IS NULL AND ($1::uuid IS NULL OR company_id = $1)
		ORDER BY id
		LIMIT $2 OFFSET $3`,
		f.CompanyID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	users, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.User, error) {
		u, err := scanUser(row)
		if err != nil {
			return model.User{}, err
		}
		return *u, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan users: %w", err)
	}
	return users, total, nil
}

// GetRoles returns the role names of several users with ONE query.
//
// Loading roles per user would be the "N+1 query" problem: 1 query for a page
// of 20 users + 20 queries for their roles. = ANY($1) takes the whole slice
// of ids as a Postgres array.
func (r *UserRepository) GetRoles(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT ur.user_id, r.name
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ANY($1)
		ORDER BY r.name`,
		userIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("get roles: %w", err)
	}
	defer rows.Close()

	roles := make(map[uuid.UUID][]string, len(userIDs))
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		roles[id] = append(roles[id], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read roles: %w", err)
	}
	return roles, nil
}

// Update saves names and is_active. If roleNames is not nil, the user's roles
// are REPLACED by exactly these, in the same transaction.
func (r *UserRepository) Update(ctx context.Context, u model.User, roleNames []string) (*model.User, error) {
	var updated *model.User

	err := pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE users
			SET first_name = $2, last_name = $3, is_active = $4
			WHERE id = $1 AND deleted_at IS NULL
			RETURNING `+userColumns,
			u.ID, u.FirstName, u.LastName, u.IsActive,
		)
		var err error
		if updated, err = scanUser(row); err != nil {
			return err
		}
		if roleNames == nil {
			return nil // roles unchanged
		}

		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, id FROM roles
			WHERE company_id IS NULL AND name = ANY($2)`,
			u.ID, roleNames,
		)
		if err != nil {
			return err
		}
		if int(tag.RowsAffected()) != len(roleNames) {
			return model.ErrRoleNotFound // rolls back the DELETE too
		}
		return nil
	})
	if err != nil {
		return nil, mapUserError("update user", err)
	}
	return updated, nil
}

// SoftDelete marks a user as deleted.
func (r *UserRepository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE users SET deleted_at = now()
		WHERE id = $1 AND deleted_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}
