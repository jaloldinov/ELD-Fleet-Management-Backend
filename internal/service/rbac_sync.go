package service

import (
	"context"
	"log/slog"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// RBACSyncer writes roles and permissions to the database
// (repository.RBACRepository).
type RBACSyncer interface {
	Sync(ctx context.Context, roles []model.RoleDef, perms []model.PermissionDef) (*model.RBACSyncResult, error)
}

// SyncRBAC makes the database match the roles and permissions defined in
// the code (model.SystemRoles, model.Permissions). main runs it at startup,
// before serving requests, so a permission added in the code is usable at
// once. See RBACRepository.Sync for the rules (it only adds).
func SyncRBAC(ctx context.Context, repo RBACSyncer, log *slog.Logger) error {
	res, err := repo.Sync(ctx, model.SystemRoles(), model.Permissions())
	if err != nil {
		return err
	}
	if res.Changed() {
		log.Info("roles and permissions synced",
			"roles_added", res.RolesAdded,
			"permissions_added", res.PermissionsAdded,
			"grants_added", res.GrantsAdded)
	}
	if len(res.Orphaned) > 0 {
		log.Warn("permissions in the database are no longer defined in the code; remove them with a migration",
			"permissions", res.Orphaned)
	}
	return nil
}
