package model

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Same rule as the permissions_code_format CHECK constraint.
var permissionFormat = regexp.MustCompile(`^[a-z_]+:[a-z_]+$`)

// Same rule as the roles_name_format CHECK constraint.
var roleNameFormat = regexp.MustCompile(`^[a-z_]{2,50}$`)

func TestPermissions_FormatUniqueAndDescribed(t *testing.T) {
	seen := make(map[Permission]bool)

	for _, p := range Permissions() {
		assert.Regexp(t, permissionFormat, string(p.Code), "permission %q must look like resource:action", p.Code)
		assert.False(t, seen[p.Code], "permission %q is defined twice", p.Code)
		assert.NotEmpty(t, p.Description, "permission %q needs a description", p.Code)
		seen[p.Code] = true
	}
	assert.Len(t, AllPermissions(), len(seen))
}

// A typo in DefaultRoles would otherwise silently grant nothing.
func TestPermissions_DefaultRolesAreCompanySystemRoles(t *testing.T) {
	companyRoles := SystemRolesByScope(RoleScopeCompany)

	for _, p := range Permissions() {
		for _, r := range p.DefaultRoles {
			assert.True(t, companyRoles[r],
				"%s: default role %q must be a company system role (platform_admin gets everything anyway)", p.Code, r)
		}
	}
}

func TestSystemRoles_Valid(t *testing.T) {
	seen := make(map[string]bool)

	for _, r := range SystemRoles() {
		assert.Regexp(t, roleNameFormat, r.Name)
		assert.Contains(t, []string{RoleScopePlatform, RoleScopeCompany}, r.Scope, r.Name)
		assert.NotEmpty(t, r.Description, r.Name)
		assert.False(t, seen[r.Name], "role %q is defined twice", r.Name)
		seen[r.Name] = true
	}
	assert.True(t, seen[RolePlatformAdmin], "platform_admin must exist")
}

func TestSystemRolesByScope(t *testing.T) {
	assert.Equal(t, map[string]bool{RolePlatformAdmin: true}, SystemRolesByScope(RoleScopePlatform))
	assert.Equal(t, map[string]bool{
		RoleAdmin: true, RoleManager: true, RoleDispatcher: true, RoleDriver: true,
	}, SystemRolesByScope(RoleScopeCompany))
}

func TestPermissions_ReturnsACopy(t *testing.T) {
	p := Permissions()
	p[0].Code = "hacked:yes"

	assert.NotEqual(t, Permission("hacked:yes"), Permissions()[0].Code)
}
