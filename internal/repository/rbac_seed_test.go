package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// These tests guard the contract between the seed migration and the Go code.

// TestPermissionsSeedMatchesCode fails when someone adds a permission constant
// without a seed migration, or seeds a permission the code never uses.
func TestPermissionsSeedMatchesCode(t *testing.T) {
	db := newTestDB(t)

	rows, err := db.Query(context.Background(), `SELECT code FROM permissions`)
	require.NoError(t, err)
	inDB, err := pgx.CollectRows(rows, pgx.RowTo[model.Permission])
	require.NoError(t, err)

	assert.ElementsMatch(t, model.AllPermissions(), inDB,
		"permissions table and model.AllPermissions() differ: add a seed migration or a constant")
}

func TestSystemRolesSeeded(t *testing.T) {
	db := newTestDB(t)

	rows, err := db.Query(context.Background(),
		`SELECT name, scope FROM roles WHERE company_id IS NULL`)
	require.NoError(t, err)
	type role struct{ Name, Scope string }
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[role])
	require.NoError(t, err)

	assert.ElementsMatch(t, []role{
		{model.RolePlatformAdmin, model.RoleScopePlatform},
		{model.RoleAdmin, model.RoleScopeCompany},
		{model.RoleManager, model.RoleScopeCompany},
		{model.RoleDispatcher, model.RoleScopeCompany},
		{model.RoleDriver, model.RoleScopeCompany},
	}, got)
}

func TestPlatformAdminHasEveryPermission(t *testing.T) {
	db := newTestDB(t)

	var count int
	err := db.QueryRow(context.Background(), `
		SELECT count(*)
		FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.company_id IS NULL AND r.name = $1`,
		model.RolePlatformAdmin,
	).Scan(&count)

	require.NoError(t, err)
	assert.Equal(t, len(model.AllPermissions()), count)
}
