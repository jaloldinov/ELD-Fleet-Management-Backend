package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// TruckService is what TruckHandler needs from the service layer.
type TruckService interface {
	Create(ctx context.Context, p auth.Principal, in service.CreateTruckInput) (*model.Truck, error)
	Get(ctx context.Context, p auth.Principal, id uuid.UUID) (*model.Truck, error)
	List(ctx context.Context, p auth.Principal, params service.TruckListParams) (*service.TruckPage, error)
	Update(ctx context.Context, p auth.Principal, id uuid.UUID, in service.UpdateTruckInput) (*model.Truck, error)
	Delete(ctx context.Context, p auth.Principal, id uuid.UUID) error
}

// ---------- DTOs ----------

type CreateTruckRequest struct {
	// Platform users: required. Company users: omit (their own company is used).
	CompanyID *uuid.UUID `json:"company_id,omitempty" swaggertype:"string" format:"uuid"`
	// The carrier's own truck number.
	UnitNumber string `json:"unit_number" example:"101"`
	// 17 characters; the check digit is validated. Cannot be changed later.
	VIN          string `json:"vin" example:"1M8GDM9AXKP042788"`
	Make         string `json:"make,omitempty" example:"Freightliner"`
	Model        string `json:"model,omitempty" example:"Cascadia"`
	Year         *int   `json:"year,omitempty" example:"2022"`
	LicensePlate string `json:"license_plate,omitempty" example:"ABC1234"`
	PlateState   string `json:"plate_state,omitempty" example:"TX"`
}

// UpdateTruckRequest: omitted fields are unchanged. The VIN cannot be changed.
// To remove the plate, send license_plate and plate_state as "".
type UpdateTruckRequest struct {
	UnitNumber   *string `json:"unit_number,omitempty" example:"101A"`
	Make         *string `json:"make,omitempty" example:"Volvo"`
	Model        *string `json:"model,omitempty" example:"VNL 860"`
	Year         *int    `json:"year,omitempty" example:"2023"`
	LicensePlate *string `json:"license_plate,omitempty" example:"XYZ9876"`
	PlateState   *string `json:"plate_state,omitempty" example:"OK"`
	IsActive     *bool   `json:"is_active,omitempty" example:"false"`
}

type TruckResponse struct {
	ID           uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	CompanyID    uuid.UUID `json:"company_id" swaggertype:"string" format:"uuid"`
	UnitNumber   string    `json:"unit_number" example:"101"`
	VIN          string    `json:"vin" example:"1M8GDM9AXKP042788"`
	Make         string    `json:"make" example:"Freightliner"`
	Model        string    `json:"model" example:"Cascadia"`
	Year         *int      `json:"year" example:"2022"`
	LicensePlate *string   `json:"license_plate" example:"ABC1234"`
	PlateState   *string   `json:"plate_state" example:"TX"`
	IsActive     bool      `json:"is_active" example:"true"`
	CreatedAt    time.Time `json:"created_at" example:"2026-10-02T11:26:26Z"`
	UpdatedAt    time.Time `json:"updated_at" example:"2026-10-02T11:26:26Z"`
}

func newTruckResponse(t *model.Truck) TruckResponse {
	return TruckResponse{
		ID: t.ID, CompanyID: t.CompanyID, UnitNumber: t.UnitNumber, VIN: t.VIN,
		Make: t.Make, Model: t.Model, Year: t.Year,
		LicensePlate: t.LicensePlate, PlateState: t.PlateState, IsActive: t.IsActive,
		CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC(),
	}
}

type ListTrucksResponse struct {
	Items    []TruckResponse `json:"items"`
	Page     int             `json:"page" example:"1"`
	PageSize int             `json:"page_size" example:"20"`
	Total    int             `json:"total" example:"14"`
}

// ---------- Handler ----------

type TruckHandler struct {
	svc TruckService
}

func NewTruckHandler(svc TruckService) *TruckHandler {
	return &TruckHandler{svc: svc}
}

// Create handles POST /api/v1/trucks.
//
//	@Summary		Create a truck
//	@Description	Requires permission: truck:create
//	@Tags			trucks
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		CreateTruckRequest	true	"Truck to create"
//	@Success		201		{object}	TruckResponse
//	@Failure		400		{object}	apierror.Response	"invalid JSON or validation failed"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission truck:create"
//	@Failure		404		{object}	apierror.Response	"company not found"
//	@Failure		409		{object}	apierror.Response	"VIN or unit number already used"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/trucks [post]
func (h *TruckHandler) Create(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req CreateTruckRequest
	if !bindJSON(c, &req) {
		return
	}

	t, err := h.svc.Create(c.Request.Context(), p, service.CreateTruckInput{
		CompanyID: req.CompanyID, UnitNumber: req.UnitNumber, VIN: req.VIN,
		Make: req.Make, Model: req.Model, Year: req.Year,
		LicensePlate: req.LicensePlate, PlateState: req.PlateState,
	})
	if err != nil {
		handleError(c, err)
		return
	}

	c.Header("Location", "/api/v1/trucks/"+t.ID.String())
	c.JSON(http.StatusCreated, newTruckResponse(t))
}

// List handles GET /api/v1/trucks.
//
//	@Summary		List trucks
//	@Description	Requires permission: truck:read
//	@Description	Company users always get their own company's trucks (company_id is ignored).
//	@Tags			trucks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			company_id	query		string	false	"Filter by company (platform users only)"	format(uuid)
//	@Param			is_active	query		bool	false	"Only active (true) or inactive (false) trucks"
//	@Param			page		query		int		false	"Page number (1-based)"	default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"		default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListTrucksResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission truck:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/trucks [get]
func (h *TruckHandler) List(c *gin.Context) {
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
	var isActive *bool
	if s := c.Query("is_active"); s != "" {
		b, err := strconv.ParseBool(s)
		v.Check(err == nil, "is_active", "must be true or false")
		isActive = &b
	}
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	res, err := h.svc.List(c.Request.Context(), p, service.TruckListParams{
		CompanyID: companyID, IsActive: isActive, Page: page, PageSize: size,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]TruckResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newTruckResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListTrucksResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

// Get handles GET /api/v1/trucks/:id.
//
//	@Summary		Get a truck
//	@Description	Requires permission: truck:read
//	@Tags			trucks
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Truck ID"	format(uuid)
//	@Success		200	{object}	TruckResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission truck:read"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/trucks/{id} [get]
func (h *TruckHandler) Get(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	t, err := h.svc.Get(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newTruckResponse(t))
}

// Update handles PATCH /api/v1/trucks/:id.
//
//	@Summary		Update a truck
//	@Description	Requires permission: truck:update
//	@Description	Partial update. The VIN cannot be changed.
//	@Tags			trucks
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Truck ID"	format(uuid)
//	@Param			body	body		UpdateTruckRequest	true	"Fields to change"
//	@Success		200		{object}	TruckResponse
//	@Failure		400		{object}	apierror.Response
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403		{object}	apierror.Response	"missing permission truck:update"
//	@Failure		404		{object}	apierror.Response
//	@Failure		409		{object}	apierror.Response	"unit number already used"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/trucks/{id} [patch]
func (h *TruckHandler) Update(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req UpdateTruckRequest
	if !bindJSON(c, &req) {
		return
	}

	t, err := h.svc.Update(c.Request.Context(), p, id, service.UpdateTruckInput{
		UnitNumber: req.UnitNumber, Make: req.Make, Model: req.Model, Year: req.Year,
		LicensePlate: req.LicensePlate, PlateState: req.PlateState, IsActive: req.IsActive,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newTruckResponse(t))
}

// Delete handles DELETE /api/v1/trucks/:id.
//
//	@Summary		Delete a truck
//	@Description	Requires permission: truck:delete
//	@Description	Soft delete; the VIN and unit number can be registered again afterwards.
//	@Tags			trucks
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Truck ID"	format(uuid)
//	@Success		204	"No Content"
//	@Failure		400	{object}	apierror.Response
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403	{object}	apierror.Response	"missing permission truck:delete"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/trucks/{id} [delete]
func (h *TruckHandler) Delete(c *gin.Context) {
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
