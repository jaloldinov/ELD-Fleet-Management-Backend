package auth

import (
	"context"

	"github.com/google/uuid"
)

// Principal is the authenticated caller of the current request: WHO is
// calling. It comes from a verified access token. (WHAT they may do is decided
// separately, by permissions.)
//
// It lives in this package, not in middleware, so services can read it
// without importing HTTP code.
type Principal struct {
	UserID    uuid.UUID
	CompanyID *uuid.UUID // nil = platform user
}

// IsPlatformUser reports whether the caller works for the platform rather
// than for a company.
func (p Principal) IsPlatformUser() bool {
	return p.CompanyID == nil
}

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext returns the caller, and false if the request is not
// authenticated.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
