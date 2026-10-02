package middleware

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/handler"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

// PermissionLoader returns a user's effective permissions
// (service.UserService.GetPermissions).
type PermissionLoader interface {
	GetPermissions(ctx context.Context, userID uuid.UUID) ([]model.Permission, error)
}

// PermissionChecker builds RequirePermission middleware. Create one in main and
// use it in the router: perms.Require(model.PermCompanyCreate).
type PermissionChecker struct {
	loader PermissionLoader
}

func NewPermissionChecker(loader PermissionLoader) *PermissionChecker {
	return &PermissionChecker{loader: loader}
}

// Require answers "may the caller do this?". It must run after Authenticate.
//
//   - no principal (wiring bug)      → 401 (fail closed)
//   - permissions cannot be loaded   → 500
//   - permission missing             → 403
func (pc *PermissionChecker) Require(perm model.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFromContext(c.Request.Context())
		if !ok {
			handler.RespondError(c, http.StatusUnauthorized, handler.CodeUnauthorized, "authentication required", nil)
			return
		}

		perms, err := pc.permissions(c, p.UserID)
		if err != nil {
			_ = c.Error(err)
			handler.RespondError(c, http.StatusInternalServerError, handler.CodeInternal, "internal server error", nil)
			return
		}

		if !perms.Has(perm) {
			// Recorded for the request log: who was denied what.
			_ = c.Error(fmt.Errorf("permission denied: %s", perm))
			handler.RespondError(c, http.StatusForbidden, handler.CodeForbidden,
				"you do not have permission to perform this action",
				map[string]string{"permission": string(perm)})
			return
		}

		c.Next()
	}
}

// permissions returns the caller's permissions, loading them from the
// database only once per request and caching them in the request context.
func (pc *PermissionChecker) permissions(c *gin.Context, userID uuid.UUID) (auth.Permissions, error) {
	if cached, ok := auth.PermissionsFromContext(c.Request.Context()); ok {
		return cached, nil
	}

	loaded, err := pc.loader.GetPermissions(c.Request.Context(), userID)
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}

	// Even an empty (or nil) list is cached: "loaded, user has none" must not
	// trigger another query.
	perms := auth.Permissions(loaded)
	c.Request = c.Request.WithContext(auth.WithPermissions(c.Request.Context(), perms))
	return perms, nil
}
