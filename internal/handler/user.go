package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// UserService is what UserHandler needs from the service layer.
type UserService interface {
	Create(ctx context.Context, p auth.Principal, in service.CreateUserInput) (*service.UserDetails, error)
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*service.UserDetails, error)
	List(ctx context.Context, p auth.Principal, params service.UserListParams) (*service.UserPage, error)
	Update(ctx context.Context, p auth.Principal, id uuid.UUID, in service.UpdateUserInput) (*service.UserDetails, error)
	Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error
	SetPassword(ctx context.Context, p auth.Principal, id uuid.UUID, newPassword string) error
	GetProfile(ctx context.Context, id uuid.UUID) (*service.UserProfile, error)
}

type UserHandler struct {
	svc UserService
}

func NewUserHandler(svc UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// Create handles POST /api/v1/users.
//
//	@Summary		Create a user
//	@Description	Requires permission: user:create
//	@Description	Company users create users only in their own company, with company roles.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateUserRequest	true	"User to create"
//	@Success		201		{object}	UserResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission user:create"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		409		{object}	apierror.Response	"email or username already used"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/users [post]
func (h *UserHandler) Create(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req CreateUserRequest
	if !bindJSON(c, &req) {
		return
	}

	d, err := h.svc.Create(c.Request.Context(), p, req.toInput())
	if err != nil {
		handleError(c, err)
		return
	}

	c.Header("Location", "/api/v1/users/"+d.User.ID.String())
	c.JSON(http.StatusCreated, newUserResponse(d.User, d.Roles))
}

// List handles GET /api/v1/users.
//
//	@Summary		List users
//	@Description	Requires permission: user:read
//	@Description	Company users always get their own company's users (company_id is ignored).
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Param			company_id	query		string	false	"Filter by company (platform users only)"	format(uuid)
//	@Param			page		query		int		false	"Page number (1-based)"						default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"							default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListUsersResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission user:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/users [get]
func (h *UserHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}

	var v validation.Validator
	page, ok := queryInt(c, "page")
	v.Check(ok, "page", "must be an integer")
	size, ok := queryInt(c, "page_size")
	v.Check(ok, "page_size", "must be an integer")
	var companyID *uuid.UUID
	if s := c.Query("company_id"); s != "" {
		id, err := uuid.Parse(s)
		v.Check(err == nil, "company_id", "must be a valid UUID")
		companyID = &id
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	result, err := h.svc.List(c.Request.Context(), p, service.UserListParams{
		CompanyID: companyID, Page: page, PageSize: size,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newListUsersResponse(result))
}

// Get handles GET /api/v1/users/:id.
//
//	@Summary		Get a user
//	@Description	Requires permission: user:read
//	@Tags			users
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"User ID"	format(uuid)
//	@Success		200	{object}	UserResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission user:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/users/{id} [get]
func (h *UserHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	d, err := h.svc.Get(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newUserResponse(d.User, d.Roles))
}

// Update handles PATCH /api/v1/users/:id.
//
//	@Summary		Update a user
//	@Description	Requires permission: user:update
//	@Description	Partial update. roles replaces all roles. Deactivating a user ends all their sessions.
//	@Description	Nobody can change their own roles or active status.
//	@Tags			users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"User ID"	format(uuid)
//	@Param			body	body		UpdateUserRequest	true	"Fields to change"
//	@Success		200		{object}	UserResponse
//	@Failure		400		{object}	apierror.Response
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission user:update, or changing your own roles/status"
//	@Failure		404		{object}	apierror.Response
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/users/{id} [patch]
func (h *UserHandler) Update(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateUserRequest
	if !bindJSON(c, &req) {
		return
	}

	d, err := h.svc.Update(c.Request.Context(), p, id, req.toInput())
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newUserResponse(d.User, d.Roles))
}

// Delete handles DELETE /api/v1/users/:id.
//
//	@Summary		Delete a user
//	@Description	Requires permission: user:delete
//	@Description	Soft delete; all of the user's sessions end. You cannot delete yourself.
//	@Tags			users
//	@Security		BearerAuth
//	@Param			id	path	string	true	"User ID"	format(uuid)
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission user:delete, or deleting yourself"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/users/{id} [delete]
func (h *UserHandler) Delete(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	if err := h.svc.Delete(c.Request.Context(), p, id); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Me handles GET /api/v1/auth/me.
//
//	@Summary		Current user
//	@Description	Returns the authenticated user, their roles and effective permissions.
//	@Tags			auth
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	MeResponse
//	@Failure		401	{object}	apierror.Response	"missing/invalid token, or the user no longer exists"
//	@Failure		403	{object}	apierror.Response	"account is disabled"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/auth/me [get]
func (h *UserHandler) Me(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}

	profile, err := h.svc.GetProfile(c.Request.Context(), p.UserID)
	if errors.Is(err, model.ErrNotFound) {
		// Valid token, but the user was deleted after it was issued.
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, "user no longer exists", nil)
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
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, "authentication required", nil)
	}
	return p, ok
}

// SetPassword handles PUT /api/v1/users/:id/password.
//
//	@Summary		Set a user's password
//	@Description	Requires permission: user:set_password
//	@Description	For users who cannot use "forgot password" (e.g. drivers without an email address) or are locked out.
//	@Description	The user is logged out everywhere. Not for your own account, nor for disabled accounts (activate them first).
//	@Tags			users
//	@Accept			json
//	@Security		BearerAuth
//	@Param			id		path	string				true	"User ID"	format(uuid)
//	@Param			body	body	SetPasswordRequest	true	"New password"
//	@Success		204		"No Content"
//	@Failure		400		{object}	apierror.Response	"invalid id, invalid JSON or weak new_password"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission user:set_password, your own account, or account is disabled"
//	@Failure		404		{object}	apierror.Response
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/users/{id}/password [put]
func (h *UserHandler) SetPassword(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req SetPasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.svc.SetPassword(c.Request.Context(), p, id, req.NewPassword); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
