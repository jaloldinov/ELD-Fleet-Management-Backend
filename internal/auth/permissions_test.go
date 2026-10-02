package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func TestPermissions_Has(t *testing.T) {
	ps := Permissions{model.PermTruckRead, model.PermDriverRead}

	assert.True(t, ps.Has(model.PermTruckRead))
	assert.False(t, ps.Has(model.PermTruckCreate))
	assert.False(t, Permissions(nil).Has(model.PermTruckRead))
}

func TestPermissions_Context(t *testing.T) {
	_, ok := PermissionsFromContext(context.Background())
	assert.False(t, ok, "not loaded yet")

	// An EMPTY list is still "loaded": the user simply has no permissions.
	got, ok := PermissionsFromContext(WithPermissions(context.Background(), Permissions{}))
	assert.True(t, ok)
	assert.Empty(t, got)
}
