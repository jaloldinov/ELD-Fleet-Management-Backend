package repository

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// These tests guard the contract between the code's registry
// (model.Permissions, model.SystemRoles) and the database.

var bgCtx = context.Background()

func syncRBAC(t *testing.T, db *pgxpool.Pool, roles []model.RoleDef, perms []model.PermissionDef) *model.RBACSyncResult {
	t.Helper()
	res, err := NewRBACRepository(db).Sync(bgCtx, roles, perms)
	require.NoError(t, err)
	return res
}

// grantsOf returns the permission codes a system role holds.
func grantsOf(t *testing.T, db *pgxpool.Pool, role string) []string {
	t.Helper()
	rows, err := db.Query(bgCtx, `
		SELECT rp.permission_code FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.company_id IS NULL AND r.name = $1
		ORDER BY 1`, role)
	require.NoError(t, err)
	codes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return codes
}

// After a sync (newTestDB runs one), the database is exactly the registry:
// same permissions and descriptions, same system roles, same grants.
func TestRBACSync_DatabaseMatchesRegistry(t *testing.T) {
	db := newTestDB(t)

	type perm struct{ Code, Description string }
	rows, err := db.Query(bgCtx, `SELECT code, description FROM permissions`)
	require.NoError(t, err)
	gotPerms, err := pgx.CollectRows(rows, pgx.RowToStructByPos[perm])
	require.NoError(t, err)
	var wantPerms []perm
	for _, p := range model.Permissions() {
		wantPerms = append(wantPerms, perm{string(p.Code), p.Description})
	}
	assert.ElementsMatch(t, wantPerms, gotPerms)

	type role struct{ Name, Scope, Description string }
	rows, err = db.Query(bgCtx, `SELECT name, scope, description FROM roles WHERE company_id IS NULL`)
	require.NoError(t, err)
	gotRoles, err := pgx.CollectRows(rows, pgx.RowToStructByPos[role])
	require.NoError(t, err)
	var wantRoles []role
	for _, r := range model.SystemRoles() {
		wantRoles = append(wantRoles, role{r.Name, r.Scope, r.Description})
	}
	assert.ElementsMatch(t, wantRoles, gotRoles)

	// Grants: platform_admin has everything; every other role exactly the
	// permissions that list it. (A grant in the database that the registry
	// does not list means the registry no longer describes reality.)
	want := map[string][]string{}
	for _, p := range model.Permissions() {
		want[model.RolePlatformAdmin] = append(want[model.RolePlatformAdmin], string(p.Code))
		for _, r := range p.DefaultRoles {
			want[r] = append(want[r], string(p.Code))
		}
	}
	for _, r := range model.SystemRoles() {
		assert.ElementsMatch(t, want[r.Name], grantsOf(t, db, r.Name), "grants of %s", r.Name)
	}
}

func TestRBACSync_SecondRunChangesNothing(t *testing.T) {
	db := newTestDB(t)

	res := syncRBAC(t, db, model.SystemRoles(), model.Permissions())

	assert.False(t, res.Changed(), "%+v", res)
	assert.Empty(t, res.Orphaned)
}

// The point of the registry: one line in Go, and the next start adds the
// permission and grants it.
func TestRBACSync_NewPermission(t *testing.T) {
	db := newTestDB(t)
	t.Cleanup(func() { _, _ = db.Exec(bgCtx, `DELETE FROM permissions WHERE code = 'test:report'`) })
	perms := append(model.Permissions(), model.PermissionDef{
		Code: "test:report", Description: "Test reports", DefaultRoles: []string{model.RoleManager},
	})

	res := syncRBAC(t, db, model.SystemRoles(), perms)

	assert.Equal(t, []model.Permission{"test:report"}, res.PermissionsAdded)
	assert.Equal(t, 2, res.GrantsAdded, "platform_admin + manager")
	assert.Contains(t, grantsOf(t, db, model.RolePlatformAdmin), "test:report")
	assert.Contains(t, grantsOf(t, db, model.RoleManager), "test:report")
	assert.NotContains(t, grantsOf(t, db, model.RoleDispatcher), "test:report")
}

// Default roles apply when a permission is created. Afterwards its grants
// belong to the database: a revoked grant stays revoked.
func TestRBACSync_DoesNotRestoreARevokedGrant(t *testing.T) {
	db := newTestDB(t)
	revoke := `DELETE FROM role_permissions WHERE permission_code = 'truck:read'
		AND role_id = (SELECT id FROM roles WHERE company_id IS NULL AND name = 'dispatcher')`
	_, err := db.Exec(bgCtx, revoke)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(bgCtx, `INSERT INTO role_permissions (role_id, permission_code)
			SELECT id, 'truck:read' FROM roles WHERE company_id IS NULL AND name = 'dispatcher'`)
	})

	res := syncRBAC(t, db, model.SystemRoles(), model.Permissions())

	assert.False(t, res.Changed())
	assert.NotContains(t, grantsOf(t, db, model.RoleDispatcher), "truck:read")
}

// platform_admin is the exception: it always holds every permission.
func TestRBACSync_RestoresPlatformAdminGrants(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Exec(bgCtx, `DELETE FROM role_permissions WHERE permission_code = 'truck:read'
		AND role_id = (SELECT id FROM roles WHERE company_id IS NULL AND name = 'platform_admin')`)
	require.NoError(t, err)

	res := syncRBAC(t, db, model.SystemRoles(), model.Permissions())

	assert.Equal(t, 1, res.GrantsAdded)
	assert.Contains(t, grantsOf(t, db, model.RolePlatformAdmin), "truck:read")
}

// A new role gets the existing permissions that list it.
func TestRBACSync_NewRole(t *testing.T) {
	db := newTestDB(t)
	t.Cleanup(func() { _, _ = db.Exec(bgCtx, `DELETE FROM roles WHERE company_id IS NULL AND name = 'test_safety'`) })
	roles := append(model.SystemRoles(), model.RoleDef{Name: "test_safety", Scope: model.RoleScopeCompany, Description: "Test"})
	perms := model.Permissions()
	for i := range perms {
		if perms[i].Code == model.PermDriverRead {
			perms[i].DefaultRoles = append(perms[i].DefaultRoles, "test_safety")
		}
	}

	res := syncRBAC(t, db, roles, perms)

	assert.Equal(t, []string{"test_safety"}, res.RolesAdded)
	assert.Empty(t, res.PermissionsAdded)
	assert.Equal(t, []string{"driver:read"}, grantsOf(t, db, "test_safety"))
}

func TestRBACSync_UpdatesDescriptions(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Exec(bgCtx, `UPDATE permissions SET description = 'old text' WHERE code = 'truck:read'`)
	require.NoError(t, err)

	syncRBAC(t, db, model.SystemRoles(), model.Permissions())

	var desc string
	require.NoError(t, db.QueryRow(bgCtx, `SELECT description FROM permissions WHERE code = 'truck:read'`).Scan(&desc))
	assert.Equal(t, "View trucks", desc)
}

func TestRBACSync_ReportsButKeepsOrphans(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Exec(bgCtx, `INSERT INTO permissions (code, description) VALUES ('test:old', 'Removed from code')`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(bgCtx, `DELETE FROM permissions WHERE code = 'test:old'`) })

	res := syncRBAC(t, db, model.SystemRoles(), model.Permissions())

	assert.Equal(t, []string{"test:old"}, res.Orphaned)
	var n int
	require.NoError(t, db.QueryRow(bgCtx, `SELECT count(*) FROM permissions WHERE code = 'test:old'`).Scan(&n))
	assert.Equal(t, 1, n, "never deleted automatically")
}

func TestRBACSync_ScopeMismatchIsAnError(t *testing.T) {
	db := newTestDB(t)
	roles := model.SystemRoles()
	roles[1].Scope = model.RoleScopePlatform // "admin" is a company role in the database

	_, err := NewRBACRepository(db).Sync(bgCtx, roles, model.Permissions())

	assert.ErrorContains(t, err, "scope")
}

// Several API instances starting at once must not fail or double-insert.
func TestRBACSync_ConcurrentInstances(t *testing.T) {
	db := newTestDB(t)
	t.Cleanup(func() { _, _ = db.Exec(bgCtx, `DELETE FROM permissions WHERE code = 'test:concurrent'`) })
	perms := append(model.Permissions(), model.PermissionDef{Code: "test:concurrent", Description: "Test"})

	results := make(chan *model.RBACSyncResult, 5)
	errs := make(chan error, 5)
	for range 5 {
		go func() {
			res, err := NewRBACRepository(db).Sync(bgCtx, model.SystemRoles(), perms)
			errs <- err
			results <- res
		}()
	}
	added := 0
	for range 5 {
		require.NoError(t, <-errs)
		added += len((<-results).PermissionsAdded)
	}

	assert.Equal(t, 1, added, "exactly one instance added it; the others found it")
}
