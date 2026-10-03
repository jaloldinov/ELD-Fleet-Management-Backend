package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// RateLimitService is what RateLimitHandler needs from the service layer.
type RateLimitService interface {
	List(ctx context.Context, p auth.Principal) ([]model.RateLimit, error)
	Get(ctx context.Context, p auth.Principal, name string) (*model.RateLimit, error)
	Update(ctx context.Context, p auth.Principal, name string, in service.UpdateRateLimitInput) (*model.RateLimit, error)
}

// ---------- DTOs ----------

// UpdateRateLimitRequest: omitted fields are unchanged.
type UpdateRateLimitRequest struct {
	// Maximum events per window, per key. 1-10000.
	Limit *int `json:"limit,omitempty" example:"5"`
	// Window length in seconds. 1-86400 (24 hours).
	WindowSeconds *int `json:"window_seconds,omitempty" example:"900"`
}

type RateLimitResponse struct {
	// auth_requests: per client IP, all /api/v1/auth/* routes.
	// login_attempts: per login name + client IP, POST /api/v1/auth/login.
	Name          string     `json:"name" example:"login_attempts"`
	Description   string     `json:"description" example:"Login attempts per login name + client IP; a successful login resets it"`
	Limit         int        `json:"limit" example:"5"`
	WindowSeconds int        `json:"window_seconds" example:"900"`
	UpdatedAt     time.Time  `json:"updated_at" example:"2026-10-03T11:26:26Z"`
	UpdatedBy     *uuid.UUID `json:"updated_by" swaggertype:"string" format:"uuid"`
}

func newRateLimitResponse(rl *model.RateLimit) RateLimitResponse {
	return RateLimitResponse{
		Name: rl.Name, Description: rl.Description,
		Limit: rl.Limit, WindowSeconds: int(rl.Window / time.Second),
		UpdatedAt: rl.UpdatedAt.UTC(), UpdatedBy: rl.UpdatedBy,
	}
}

type ListRateLimitsResponse struct {
	Items []RateLimitResponse `json:"items"`
}

// ---------- Handler ----------

type RateLimitHandler struct {
	svc RateLimitService
}

func NewRateLimitHandler(svc RateLimitService) *RateLimitHandler {
	return &RateLimitHandler{svc: svc}
}

// List handles GET /api/v1/rate-limits.
//
//	@Summary		List rate limits
//	@Description	Requires permission: ratelimit:read (platform users only)
//	@Tags			rate-limits
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	ListRateLimitsResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission ratelimit:read, or not a platform user"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/rate-limits [get]
func (h *RateLimitHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	limits, err := h.svc.List(c.Request.Context(), p)
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]RateLimitResponse, 0, len(limits))
	for i := range limits {
		items = append(items, newRateLimitResponse(&limits[i]))
	}
	c.JSON(http.StatusOK, ListRateLimitsResponse{Items: items})
}

// Get handles GET /api/v1/rate-limits/:name.
//
//	@Summary		Get a rate limit
//	@Description	Requires permission: ratelimit:read (platform users only)
//	@Tags			rate-limits
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string	true	"Rule name"	Enums(auth_requests, login_attempts)
//	@Success		200		{object}	RateLimitResponse
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission ratelimit:read, or not a platform user"
//	@Failure		404		{object}	apierror.Response	"unknown rule"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/rate-limits/{name} [get]
func (h *RateLimitHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	rl, err := h.svc.Get(c.Request.Context(), p, c.Param("name"))
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newRateLimitResponse(rl))
}

// Update handles PATCH /api/v1/rate-limits/:name.
//
//	@Summary		Update a rate limit
//	@Description	Requires permission: ratelimit:update (platform users only)
//	@Description	Partial update. Saved in the database and applied immediately on this instance; other instances apply it within a minute.
//	@Description	Keys that already used up their attempts are not reset: their tokens come back at the new rate.
//	@Tags			rate-limits
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			name	path		string					true	"Rule name"	Enums(auth_requests, login_attempts)
//	@Param			body	body		UpdateRateLimitRequest	true	"Fields to change"
//	@Success		200		{object}	RateLimitResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission ratelimit:update, or not a platform user"
//	@Failure		404		{object}	apierror.Response	"unknown rule"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/rate-limits/{name} [patch]
func (h *RateLimitHandler) Update(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req UpdateRateLimitRequest
	if !bindJSON(c, &req) {
		return
	}

	rl, err := h.svc.Update(c.Request.Context(), p, c.Param("name"), service.UpdateRateLimitInput{
		Limit: req.Limit, WindowSeconds: req.WindowSeconds,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newRateLimitResponse(rl))
}
