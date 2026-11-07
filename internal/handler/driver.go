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
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// DriverService is what DriverHandler needs from the service layer.
type DriverService interface {
	Create(ctx context.Context, p auth.Principal, in service.CreateDriverInput) (*model.DriverDetails, error)
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.DriverDetails, error)
	Me(ctx context.Context, p auth.Principal) (*model.DriverDetails, error)
	List(ctx context.Context, p auth.Principal, params service.DriverListParams) (*service.DriverPage, error)
	Update(ctx context.Context, p auth.Principal, id uuid.UUID, in service.UpdateDriverInput) (*model.DriverDetails, error)
	Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error
}

// ---------- DTOs ----------

type CreateDriverRequest struct {
	// Platform users: required. Company users: omit (their own company is used).
	CompanyID *uuid.UUID `json:"company_id,omitempty" swaggertype:"string" format:"uuid"`
	// Email and/or username. Drivers often have only a username.
	Email     string `json:"email,omitempty" example:""`
	Username  string `json:"username,omitempty" example:"driver.bob"`
	Password  string `json:"password" example:"correct horse battery staple"`
	FirstName string `json:"first_name" example:"Bob"`
	LastName  string `json:"last_name" example:"Lee"`

	LicenseNumber string `json:"license_number" example:"D1234567"`
	// Two-letter US state code.
	LicenseState string `json:"license_state" example:"TX"`
	// Calendar day, YYYY-MM-DD.
	LicenseExpiresAt string `json:"license_expires_at" example:"2028-05-31"`
}

// UpdateDriverRequest changes license data only. To rename or deactivate a
// driver, use PATCH /api/v1/users/{user_id}.
type UpdateDriverRequest struct {
	LicenseNumber    *string `json:"license_number,omitempty" example:"D7654321"`
	LicenseState     *string `json:"license_state,omitempty" example:"OK"`
	LicenseExpiresAt *string `json:"license_expires_at,omitempty" example:"2030-01-15"`
}

type DriverResponse struct {
	ID        uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	UserID    uuid.UUID `json:"user_id" swaggertype:"string" format:"uuid"`
	CompanyID uuid.UUID `json:"company_id" swaggertype:"string" format:"uuid"`
	Email     *string   `json:"email" example:"bob@acme.com"`
	Username  *string   `json:"username" example:"driver.bob"`
	FirstName string    `json:"first_name" example:"Bob"`
	LastName  string    `json:"last_name" example:"Lee"`
	// Follows the user account: a deactivated user is an inactive driver.
	IsActive         bool   `json:"is_active" example:"true"`
	LicenseNumber    string `json:"license_number" example:"D1234567"`
	LicenseState     string `json:"license_state" example:"TX"`
	LicenseExpiresAt string `json:"license_expires_at" example:"2028-05-31"`
	// The user who dispatches this driver; null = nobody.
	DispatcherID *uuid.UUID `json:"dispatcher_id" swaggertype:"string" format:"uuid"`
	CreatedAt    time.Time  `json:"created_at" example:"2026-10-02T11:26:26Z"`
}

func newDriverResponse(dd *model.DriverDetails) DriverResponse {
	d, u := dd.Driver, dd.User
	return DriverResponse{
		ID: d.ID, UserID: d.UserID, CompanyID: d.CompanyID,
		Email: u.Email, Username: u.Username, FirstName: u.FirstName, LastName: u.LastName,
		IsActive:      u.IsActive,
		LicenseNumber: d.LicenseNumber, LicenseState: d.LicenseState,
		LicenseExpiresAt: d.LicenseExpiresAt.Format(time.DateOnly),
		DispatcherID:     d.DispatcherID,
		CreatedAt:        d.CreatedAt.UTC(),
	}
}

type ListDriversResponse struct {
	Items    []DriverResponse `json:"items"`
	Page     int              `json:"page" example:"1"`
	PageSize int              `json:"page_size" example:"20"`
	Total    int              `json:"total" example:"8"`
}

// parseDate parses an optional YYYY-MM-DD field into v's errors.
func parseDate(v *validation.Validator, field, s string) time.Time {
	if s == "" {
		return time.Time{} // missing: the service reports "is required"
	}
	t, err := time.Parse(time.DateOnly, s)
	v.Check(err == nil, field, "must be a date in YYYY-MM-DD format")
	return t
}

// ---------- Handler ----------

type DriverHandler struct {
	svc DriverService
}

func NewDriverHandler(svc DriverService) *DriverHandler {
	return &DriverHandler{svc: svc}
}

// Create handles POST /api/v1/drivers.
//
//	@Summary		Create a driver
//	@Description	Requires permission: driver:create
//	@Description	Creates the driver's user account (role "driver") and driver profile in one transaction.
//	@Tags			drivers
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateDriverRequest	true	"Driver to create"
//	@Success		201		{object}	DriverResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission driver:create"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		409		{object}	apierror.Response	"login or license already used"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers [post]
func (h *DriverHandler) Create(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req CreateDriverRequest
	if !bindJSON(c, &req) {
		return
	}
	var v validation.Validator
	expires := parseDate(&v, "license_expires_at", req.LicenseExpiresAt)
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	dd, err := h.svc.Create(c.Request.Context(), p, service.CreateDriverInput{
		CompanyID: req.CompanyID, Email: req.Email, Username: req.Username, Password: req.Password,
		FirstName: req.FirstName, LastName: req.LastName,
		LicenseNumber: req.LicenseNumber, LicenseState: req.LicenseState, LicenseExpiresAt: expires,
	})
	if err != nil {
		handleError(c, err)
		return
	}

	c.Header("Location", "/api/v1/drivers/"+dd.Driver.ID.String())
	c.JSON(http.StatusCreated, newDriverResponse(dd))
}

// List handles GET /api/v1/drivers.
//
//	@Summary		List drivers
//	@Description	Requires permission: driver:read
//	@Description	Company users always get their own company's drivers (company_id is ignored).
//	@Tags			drivers
//	@Produce		json
//	@Security		BearerAuth
//	@Param			company_id		query		string	false	"Filter by company (platform users only)"	format(uuid)
//	@Param			dispatcher_id	query		string	false	"Drivers of this dispatcher (a user id, or \"me\")"
//	@Param			page			query		int		false	"Page number (1-based)"	default(1)	minimum(1)
//	@Param			page_size		query		int		false	"Items per page"		default(20)	minimum(1)	maximum(100)
//	@Success		200				{object}	ListDriversResponse
//	@Failure		400				{object}	apierror.Response
//	@Failure		401				{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403				{object}	apierror.Response	"missing permission driver:read"
//	@Failure		500				{object}	apierror.Response
//	@Router			/api/v1/drivers [get]
func (h *DriverHandler) List(c *gin.Context) {
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
	// "me": the caller's own drivers, without having to know their user id.
	var dispatcherID *uuid.UUID
	if s := c.Query("dispatcher_id"); s == "me" {
		dispatcherID = &p.UserID
	} else if s != "" {
		id, err := uuid.Parse(s)
		v.Check(err == nil, "dispatcher_id", `must be a valid UUID or "me"`)
		dispatcherID = &id
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	res, err := h.svc.List(c.Request.Context(), p, service.DriverListParams{
		CompanyID: companyID, DispatcherID: dispatcherID, Page: page, PageSize: size})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]DriverResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newDriverResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListDriversResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

// Me handles GET /api/v1/drivers/me.
//
//	@Summary		My driver profile
//	@Description	For the driver app: the caller's own driver profile. Needs a login, no extra permission.
//	@Tags			drivers
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	DriverResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404	{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/me [get]
func (h *DriverHandler) Me(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	dd, err := h.svc.Me(c.Request.Context(), p)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverResponse(dd))
}

// Get handles GET /api/v1/drivers/:id.
//
//	@Summary		Get a driver
//	@Description	Requires permission: driver:read
//	@Tags			drivers
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Driver ID"	format(uuid)
//	@Success		200	{object}	DriverResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission driver:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/{id} [get]
func (h *DriverHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	dd, err := h.svc.Get(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverResponse(dd))
}

// Update handles PATCH /api/v1/drivers/:id.
//
//	@Summary		Update a driver's license
//	@Description	Requires permission: driver:update
//	@Description	License fields only. Names and active status: PATCH /api/v1/users/{user_id}.
//	@Tags			drivers
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Driver ID"	format(uuid)
//	@Param			body	body		UpdateDriverRequest	true	"Fields to change"
//	@Success		200		{object}	DriverResponse
//	@Failure		400		{object}	apierror.Response
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission driver:update"
//	@Failure		404		{object}	apierror.Response
//	@Failure		409		{object}	apierror.Response	"license already used"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/{id} [patch]
func (h *DriverHandler) Update(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateDriverRequest
	if !bindJSON(c, &req) {
		return
	}
	in := service.UpdateDriverInput{LicenseNumber: req.LicenseNumber, LicenseState: req.LicenseState}
	var v validation.Validator
	if req.LicenseExpiresAt != nil {
		t := parseDate(&v, "license_expires_at", *req.LicenseExpiresAt)
		in.LicenseExpiresAt = &t
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	dd, err := h.svc.Update(c.Request.Context(), p, id, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverResponse(dd))
}

// Delete handles DELETE /api/v1/drivers/:id.
//
//	@Summary		Delete a driver
//	@Description	Requires permission: driver:delete
//	@Description	Soft-deletes the driver profile and the driver's user account; all sessions end.
//	@Tags			drivers
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Driver ID"	format(uuid)
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission driver:delete"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/{id} [delete]
func (h *DriverHandler) Delete(c *gin.Context) {
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
