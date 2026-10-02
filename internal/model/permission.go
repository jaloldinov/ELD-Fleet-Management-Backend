package model

// Permission is an action a user may perform, named "resource:action".
// Handlers are protected with these constants (never raw strings), so a typo
// is a compile error instead of a silent "403 forever".
//
// Must match the rows seeded in migrations/*_seed_rbac.up.sql; the repository
// integration test TestPermissionsSeedMatchesCode fails if they drift apart.
type Permission string

const (
	PermCompanyCreate Permission = "company:create"
	PermCompanyRead   Permission = "company:read"
	PermCompanyUpdate Permission = "company:update"
	PermCompanyDelete Permission = "company:delete"

	PermUserCreate Permission = "user:create"
	PermUserRead   Permission = "user:read"
	PermUserUpdate Permission = "user:update"
	PermUserDelete Permission = "user:delete"

	PermDriverCreate Permission = "driver:create"
	PermDriverRead   Permission = "driver:read"
	PermDriverUpdate Permission = "driver:update"
	PermDriverDelete Permission = "driver:delete"

	PermTruckCreate Permission = "truck:create"
	PermTruckRead   Permission = "truck:read"
	PermTruckUpdate Permission = "truck:update"
	PermTruckDelete Permission = "truck:delete"

	PermELDCreate Permission = "eld:create"
	PermELDRead   Permission = "eld:read"
	PermELDUpdate Permission = "eld:update"
	PermELDDelete Permission = "eld:delete"
)

// AllPermissions lists every permission the code knows about.
func AllPermissions() []Permission {
	return []Permission{
		PermCompanyCreate, PermCompanyRead, PermCompanyUpdate, PermCompanyDelete,
		PermUserCreate, PermUserRead, PermUserUpdate, PermUserDelete,
		PermDriverCreate, PermDriverRead, PermDriverUpdate, PermDriverDelete,
		PermTruckCreate, PermTruckRead, PermTruckUpdate, PermTruckDelete,
		PermELDCreate, PermELDRead, PermELDUpdate, PermELDDelete,
	}
}

// System role names, seeded by migrations.
const (
	RolePlatformAdmin = "platform_admin"
	RoleAdmin         = "admin"
	RoleManager       = "manager"
	RoleDispatcher    = "dispatcher"
	RoleDriver        = "driver"
)

// Role scopes: which kind of user a role may be given to.
const (
	RoleScopePlatform = "platform" // users with no company
	RoleScopeCompany  = "company"  // users that belong to a company
)
