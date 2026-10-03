package repository

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// RBACRepository keeps the permissions and system roles in the database in
// step with the code (model.Permissions, model.SystemRoles).
type RBACRepository struct {
	db DBTX
}

func NewRBACRepository(db DBTX) *RBACRepository {
	return &RBACRepository{db: db}
}

func (r *RBACRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// rbacSyncLockKey identifies the sync's advisory lock (any fixed number).
const rbacSyncLockKey int64 = 0x454c445f52424143 // "ELD_RBAC"

// Sync makes the database contain every role and permission of the code.
// It only ADDS, never removes:
//
//   - a new role is inserted and gets every existing permission that lists
//     it in DefaultRoles;
//   - a new permission is inserted and granted to its DefaultRoles;
//   - platform_admin always holds every permission;
//   - descriptions are updated to the code's text;
//   - grants of permissions that already existed are left alone, so a grant
//     an admin revoked is not put back on the next restart;
//   - permissions the code no longer defines are reported, not deleted.
//
// It runs in one transaction under an advisory lock: when several instances
// start at once, they sync one after another, and the later ones find
// nothing to do.
func (r *RBACRepository) Sync(ctx context.Context, roles []model.RoleDef, perms []model.PermissionDef) (*model.RBACSyncResult, error) {
	var res model.RBACSyncResult
	err := pgx.BeginFunc(ctx, r.q(ctx), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacSyncLockKey); err != nil {
			return fmt.Errorf("lock: %w", err)
		}

		for _, role := range roles {
			added, err := syncRole(ctx, tx, role)
			if err != nil {
				return err
			}
			if added {
				res.RolesAdded = append(res.RolesAdded, role.Name)
			}
		}

		codes := make([]string, 0, len(perms))
		for _, p := range perms {
			codes = append(codes, string(p.Code))
			added, err := syncPermission(ctx, tx, p)
			if err != nil {
				return err
			}

			// Who gets this permission now: platform_admin always; the
			// default roles only if the permission is new, or the role is.
			grantTo := []string{model.RolePlatformAdmin}
			if added {
				res.PermissionsAdded = append(res.PermissionsAdded, p.Code)
				grantTo = append(grantTo, p.DefaultRoles...)
			} else {
				for _, role := range p.DefaultRoles {
					if slices.Contains(res.RolesAdded, role) {
						grantTo = append(grantTo, role)
					}
				}
			}

			tag, err := tx.Exec(ctx, `
				INSERT INTO role_permissions (role_id, permission_code)
				SELECT id, $2 FROM roles
				WHERE company_id IS NULL AND name = ANY($1)
				ON CONFLICT DO NOTHING`, grantTo, p.Code)
			if err != nil {
				return fmt.Errorf("grant %s: %w", p.Code, err)
			}
			res.GrantsAdded += int(tag.RowsAffected())
		}

		rows, err := tx.Query(ctx, `SELECT code FROM permissions WHERE code <> ALL($1) ORDER BY code`, codes)
		if err != nil {
			return fmt.Errorf("find orphaned permissions: %w", err)
		}
		res.Orphaned, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("find orphaned permissions: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sync rbac: %w", err)
	}
	return &res, nil
}

// syncRole inserts role if missing (added = true), otherwise updates its
// description. A role whose scope differs from the code is an error: moving
// a role between platform and company users must be a deliberate migration.
func syncRole(ctx context.Context, tx pgx.Tx, role model.RoleDef) (added bool, err error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO roles (name, scope, description) VALUES ($1, $2, $3)
		ON CONFLICT (name) WHERE company_id IS NULL DO NOTHING`,
		role.Name, role.Scope, role.Description)
	if err != nil {
		return false, fmt.Errorf("insert role %s: %w", role.Name, err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}

	var scope, description string
	if err := tx.QueryRow(ctx, `
		SELECT scope, description FROM roles
		WHERE company_id IS NULL AND name = $1`, role.Name).Scan(&scope, &description); err != nil {
		return false, fmt.Errorf("read role %s: %w", role.Name, err)
	}
	if scope != role.Scope {
		return false, fmt.Errorf("role %s has scope %q in the database but %q in the code", role.Name, scope, role.Scope)
	}
	// Only when it differs: roles.updated_at is set by a trigger, and a
	// restart must not look like an edit.
	if description != role.Description {
		if _, err := tx.Exec(ctx, `UPDATE roles SET description = $2 WHERE company_id IS NULL AND name = $1`,
			role.Name, role.Description); err != nil {
			return false, fmt.Errorf("update role %s: %w", role.Name, err)
		}
	}
	return false, nil
}

// syncPermission inserts p if missing (added = true), otherwise updates its
// description.
func syncPermission(ctx context.Context, tx pgx.Tx, p model.PermissionDef) (added bool, err error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO permissions (code, description) VALUES ($1, $2)
		ON CONFLICT (code) DO NOTHING`, p.Code, p.Description)
	if err != nil {
		return false, fmt.Errorf("insert permission %s: %w", p.Code, err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE permissions SET description = $2 WHERE code = $1 AND description <> $2`,
		p.Code, p.Description); err != nil {
		return false, fmt.Errorf("update permission %s: %w", p.Code, err)
	}
	return false, nil
}
