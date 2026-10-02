package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// UserService is what UserHandler needs from the service layer.
type UserService interface {
	GetProfile(ctx context.Context, id uuid.UUID) (*service.UserProfile, error)
}

type UserHandler struct {
	svc UserService
}

func NewUserHandler(svc UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// Me handles GET /api/v1/auth/me.
//
//	@Summary		Current user
//	@Description	Returns the authenticated user and their effective permissions.
//	@Tags			auth
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	MeResponse
//	@Failure		401	{object}	ErrorResponse	"missing/invalid token, or the user no longer exists"
//	@Failure		500	{object}	ErrorResponse
//	@Router			/api/v1/auth/me [get]
func (h *UserHandler) Me(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}

	profile, err := h.svc.GetProfile(c.Request.Context(), p.UserID)
	if errors.Is(err, model.ErrNotFound) {
		// Valid token, but the user was deleted after it was issued.
		RespondError(c, http.StatusUnauthorized, CodeUnauthorized, "user no longer exists", nil)
		return
	}
	if err != nil {
		handleError(c, err)
		return
	}

	c.JSON(http.StatusOK, newMeResponse(profile))
}

// principal returns the authenticated caller. If there is none, the route was
// registered without the Authenticate middleware (a wiring bug): fail closed
// with 401 instead of continuing as "nobody".
func principal(c *gin.Context) (auth.Principal, bool) {
	p, ok := auth.PrincipalFromContext(c.Request.Context())
	if !ok {
		RespondError(c, http.StatusUnauthorized, CodeUnauthorized, "authentication required", nil)
	}
	return p, ok
}
