package model

// Permission is an action a user may perform, named "resource:action".
// Handlers are protected with these variables (never raw strings), so a typo
// is a compile error instead of a silent "403 forever".
type Permission string

// PermissionDef describes a permission for the database sync
// (repository.RBACRepository.Sync, run at startup).
type PermissionDef struct {
	Code        Permission
	Description string
	// DefaultRoles get the permission when it is first added to the
	// database (and a role listed here gets it when the role is first
	// added). platform_admin always gets every permission: never list it.
	DefaultRoles []string
}

// permissionRegistry is filled by define, in declaration order.
var permissionRegistry []PermissionDef

// define registers a permission and returns its code. Declaring a
// permission and registering it is one line, so a permission the database
// sync does not know about cannot exist.
func define(code Permission, description string, defaultRoles ...string) Permission {
	permissionRegistry = append(permissionRegistry, PermissionDef{
		Code: code, Description: description, DefaultRoles: defaultRoles,
	})
	return code
}

// The code is the single source of truth for permissions. To add one, add a
// line here (and protect a route with it). On the next start, the sync
// inserts it and grants it to platform_admin and its default roles.
// Changing DefaultRoles later does NOT change existing grants: once a
// permission exists, its grants belong to the database (and to admins).
//
// These are variables only because define runs at startup; never assign to
// them.
var (
	PermCompanyCreate = define("company:create", "Create companies")
	PermCompanyRead   = define("company:read", "View companies", RoleAdmin, RoleManager, RoleDispatcher)
	PermCompanyUpdate = define("company:update", "Edit companies", RoleAdmin)
	PermCompanyDelete = define("company:delete", "Delete companies")

	PermUserCreate = define("user:create", "Create users", RoleAdmin)
	PermUserRead   = define("user:read", "View users", RoleAdmin, RoleManager)
	PermUserUpdate = define("user:update", "Edit users and their roles", RoleAdmin)
	PermUserDelete = define("user:delete", "Delete users", RoleAdmin)
	// Separate from user:update: setting someone's password means being able
	// to log in as them, so it is granted on its own.
	PermUserSetPassword = define("user:set_password", "Set other users' passwords", RoleAdmin)

	PermDriverCreate = define("driver:create", "Create drivers", RoleAdmin, RoleManager)
	PermDriverRead   = define("driver:read", "View drivers", RoleAdmin, RoleManager, RoleDispatcher)
	PermDriverUpdate = define("driver:update", "Edit drivers", RoleAdmin, RoleManager)
	PermDriverDelete = define("driver:delete", "Delete drivers", RoleAdmin, RoleManager)

	PermTruckCreate = define("truck:create", "Create trucks", RoleAdmin, RoleManager)
	PermTruckRead   = define("truck:read", "View trucks", RoleAdmin, RoleManager, RoleDispatcher)
	PermTruckUpdate = define("truck:update", "Edit trucks", RoleAdmin, RoleManager)
	PermTruckDelete = define("truck:delete", "Delete trucks", RoleAdmin, RoleManager)

	PermELDCreate = define("eld:create", "Register ELD devices", RoleAdmin, RoleManager)
	PermELDRead   = define("eld:read", "View ELD devices", RoleAdmin, RoleManager, RoleDispatcher)
	PermELDUpdate = define("eld:update", "Edit ELD devices", RoleAdmin, RoleManager)
	PermELDDelete = define("eld:delete", "Delete ELD devices", RoleAdmin, RoleManager)

	// The dispatch board. Putting drivers on trucks is the dispatcher's daily
	// job. Drivers see and pick their own truck without these
	// (/drivers/me/truck: identity, not a permission).
	PermAssignmentRead   = define("assignment:read", "View who drives which truck and which device is in it", RoleAdmin, RoleManager, RoleDispatcher)
	PermAssignmentManage = define("assignment:manage", "Assign drivers to trucks", RoleAdmin, RoleManager, RoleDispatcher)

	// History of changes. Company admins see their own company's.
	PermAuditRead = define("audit:read", "View the audit log", RoleAdmin)

	// Platform settings: platform_admin only.
	PermRateLimitRead   = define("ratelimit:read", "View rate-limit settings")
	PermRateLimitUpdate = define("ratelimit:update", "Change rate-limit settings")

	// ---------- Billing (Phase 2, Stripe) ----------
	// Only company admins get billing by default: it is about money, and
	// dispatchers or managers have no reason to see the company's bills.

	// The catalog (products and their prices) is the platform's: company
	// admins read it to choose a plan; only the platform changes it.
	PermProductRead   = define("product:read", "View products and prices", RoleAdmin)
	PermProductCreate = define("product:create", "Create products and prices")
	PermProductUpdate = define("product:update", "Edit products, archive prices")
	PermProductDelete = define("product:delete", "Archive products")

	// A company's own subscription: start it, change the plan, schedule a
	// change, cancel.
	PermSubscriptionRead   = define("subscription:read", "View the company subscription", RoleAdmin)
	PermSubscriptionManage = define("subscription:manage", "Start, change, schedule and cancel the subscription", RoleAdmin)

	// Cards are collected by Stripe.js; we only see their ids and last 4 digits.
	PermPaymentMethodManage = define("payment_method:manage", "View, add and remove payment methods", RoleAdmin)

	PermInvoiceRead = define("invoice:read", "View invoices", RoleAdmin)
	PermInvoicePay  = define("invoice:pay", "Pay open invoices", RoleAdmin)
	// Platform only: create one-off charges, finalize and void invoices.
	PermInvoiceManage = define("invoice:manage", "Create one-off charges, finalize and void invoices")
	// Platform only: give credit (customer balance) and credit notes.
	PermCreditManage = define("credit:manage", "Give credits and credit notes")
	// Platform only: revenue across all companies.
	PermBillingMetricsRead = define("billing_metrics:read", "View billing metrics (MRR, revenue, failed payments)")
)

// Permissions returns every registered permission, in declaration order.
func Permissions() []PermissionDef {
	out := make([]PermissionDef, len(permissionRegistry))
	copy(out, permissionRegistry)
	return out
}

// AllPermissions lists every permission code the code knows about.
func AllPermissions() []Permission {
	out := make([]Permission, 0, len(permissionRegistry))
	for _, p := range permissionRegistry {
		out = append(out, p.Code)
	}
	return out
}

// System role names.
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

// RoleDef describes a system role for the database sync.
type RoleDef struct {
	Name        string
	Scope       string // RoleScopePlatform or RoleScopeCompany
	Description string
}

// SystemRoles are the roles shared by every company, kept in the database
// by the sync. To add one, add a line here; give it permissions by listing
// it in DefaultRoles above.
func SystemRoles() []RoleDef {
	return []RoleDef{
		{RolePlatformAdmin, RoleScopePlatform, "Platform staff: manages all companies"},
		{RoleAdmin, RoleScopeCompany, "Company administrator: manages users and fleet"},
		{RoleManager, RoleScopeCompany, "Fleet manager: manages drivers, trucks and ELDs"},
		{RoleDispatcher, RoleScopeCompany, "Dispatcher: views fleet, assigns work"},
		{RoleDriver, RoleScopeCompany, "Driver: uses the driver app (permissions added in Phase 5)"},
	}
}

// SystemRolesByScope returns the system role names allowed for scope.
func SystemRolesByScope(scope string) map[string]bool {
	out := make(map[string]bool)
	for _, r := range SystemRoles() {
		if r.Scope == scope {
			out[r.Name] = true
		}
	}
	return out
}

// RBACSyncResult reports what a sync changed.
type RBACSyncResult struct {
	RolesAdded       []string
	PermissionsAdded []Permission
	GrantsAdded      int
	// Orphaned: permissions in the database that the code no longer
	// defines. Never deleted automatically (a typo must not wipe grants);
	// remove them with a migration once you are sure.
	Orphaned []string
}

// Changed reports whether the sync wrote anything.
func (r RBACSyncResult) Changed() bool {
	return len(r.RolesAdded) > 0 || len(r.PermissionsAdded) > 0 || r.GrantsAdded > 0
}
