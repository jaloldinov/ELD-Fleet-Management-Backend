package auth

import (
	"context"
	"slices"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// Permissions is the caller's effective permission list, loaded once per
// request and cached in the request context.
type Permissions []model.Permission

// Has reports whether p is in the list.
func (ps Permissions) Has(p model.Permission) bool {
	return slices.Contains(ps, p)
}

type permissionsKey struct{}

// WithPermissions returns a copy of ctx carrying the caller's permissions.
func WithPermissions(ctx context.Context, ps Permissions) context.Context {
	return context.WithValue(ctx, permissionsKey{}, ps)
}

// PermissionsFromContext returns the cached permissions, and false if they
// have not been loaded for this request yet.
func PermissionsFromContext(ctx context.Context) (Permissions, bool) {
	ps, ok := ctx.Value(permissionsKey{}).(Permissions)
	return ps, ok
}
