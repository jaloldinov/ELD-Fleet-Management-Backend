package model

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Same rule as the permissions_code_format CHECK constraint.
var permissionFormat = regexp.MustCompile(`^[a-z_]+:[a-z_]+$`)

func TestAllPermissions_FormatAndUnique(t *testing.T) {
	seen := make(map[Permission]bool)

	for _, p := range AllPermissions() {
		assert.Regexp(t, permissionFormat, string(p), "permission %q must look like resource:action", p)
		assert.False(t, seen[p], "permission %q is listed twice", p)
		seen[p] = true
	}
}
