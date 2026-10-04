// Package requestid stores and reads the current request's ID in a context.Context.
//
// It lives in its own small package (not in middleware) so that every layer
// (handler, service, repository) can read the ID without importing HTTP code.
package requestid

import "context"

// ctxKey is unexported, so no other package can create the same key and
// accidentally overwrite or read our value.
type ctxKey struct{}

// NewContext returns a copy of ctx that carries id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns the request ID stored in ctx, or "" if there is none.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
