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

// AssignmentService is what AssignmentHandler needs from the service layer.
type AssignmentService interface {
	AssignDriver(ctx context.Context, p auth.Principal, driverID uuid.UUID, in service.AssignDriverInput) (*model.DriverAssignment, error)
	AssignMe(ctx context.Context, p auth.Principal, in service.AssignDriverInput) (*model.DriverAssignment, error)
	UnassignDriver(ctx context.Context, p auth.Principal, driverID uuid.UUID) error
	UnassignMe(ctx context.Context, p auth.Principal) error
}

// ---------- DTOs ----------

type AssignDriverRequest struct {
	TruckID *uuid.UUID `json:"truck_id" swaggertype:"string" format:"uuid"`
	// Omit for primary. A truck has at most one of each.
	Role string `json:"role,omitempty" enums:"primary,co_driver" example:"primary"`
}

// DriverAssignmentResponse is one stint of a driver on a truck.
type DriverAssignmentResponse struct {
	ID        uuid.UUID  `json:"id" swaggertype:"string" format:"uuid"`
	CompanyID uuid.UUID  `json:"company_id" swaggertype:"string" format:"uuid"`
	DriverID  uuid.UUID  `json:"driver_id" swaggertype:"string" format:"uuid"`
	TruckID   uuid.UUID  `json:"truck_id" swaggertype:"string" format:"uuid"`
	Role      string     `json:"role" enums:"primary,co_driver" example:"primary"`
	StartedAt time.Time  `json:"started_at" example:"2026-11-07T08:00:00Z"`
	EndedAt   *time.Time `json:"ended_at" example:"2026-11-07T17:30:00Z"`       // null = current
	StartedBy *uuid.UUID `json:"started_by" swaggertype:"string" format:"uuid"` // null = the system
	EndedBy   *uuid.UUID `json:"ended_by" swaggertype:"string" format:"uuid"`
}

func newDriverAssignmentResponse(a *model.DriverAssignment) DriverAssignmentResponse {
	r := DriverAssignmentResponse{
		ID: a.ID, CompanyID: a.CompanyID, DriverID: a.DriverID, TruckID: a.TruckID, Role: string(a.Role),
		StartedAt: a.StartedAt.UTC(), StartedBy: a.StartedBy, EndedBy: a.EndedBy,
	}
	if a.EndedAt != nil {
		ended := a.EndedAt.UTC()
		r.EndedAt = &ended
	}
	return r
}

// ---------- Handler ----------

type AssignmentHandler struct {
	svc AssignmentService
}

func NewAssignmentHandler(svc AssignmentService) *AssignmentHandler {
	return &AssignmentHandler{svc: svc}
}

// Assign handles PUT /api/v1/drivers/:id/truck.
//
//	@Summary		Put a driver on a truck
//	@Description	Requires permission: assignment:manage
//	@Description	Puts the driver on a truck of the same company, or moves them there from their current truck (the old stint ends). Sending what is already true changes nothing.
//	@Tags			assignments
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Driver ID"	format(uuid)
//	@Param			body	body		AssignDriverRequest	true	"Truck and role"
//	@Success		200		{object}	DriverAssignmentResponse
//	@Failure		400		{object}	apierror.Response	"truck not found or out of service, invalid role, or driver account disabled"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402		{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403		{object}	apierror.Response	"missing permission assignment:manage"
//	@Failure		404		{object}	apierror.Response	"driver not found"
//	@Failure		409		{object}	apierror.Response	"the truck already has a driver in that role"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/{id}/truck [put]
func (h *AssignmentHandler) Assign(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	in, ok := bindAssignDriver(c)
	if !ok {
		return
	}
	a, err := h.svc.AssignDriver(c.Request.Context(), p, id, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverAssignmentResponse(a))
}

// AssignMe handles PUT /api/v1/drivers/me/truck.
//
//	@Summary		Pick my truck
//	@Description	For the driver app: the caller (a driver) picks the truck they drive. Needs a login, no extra permission. Moves the driver if they were on another truck.
//	@Tags			assignments
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		AssignDriverRequest	true	"Truck and role"
//	@Success		200		{object}	DriverAssignmentResponse
//	@Failure		400		{object}	apierror.Response	"truck not found or out of service, or invalid role"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402		{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		404		{object}	apierror.Response	"the caller is not a driver"
//	@Failure		409		{object}	apierror.Response	"the truck already has a driver in that role"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/truck [put]
func (h *AssignmentHandler) AssignMe(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	in, ok := bindAssignDriver(c)
	if !ok {
		return
	}
	a, err := h.svc.AssignMe(c.Request.Context(), p, in)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverAssignmentResponse(a))
}

// Unassign handles DELETE /api/v1/drivers/:id/truck.
//
//	@Summary		Take a driver off their truck
//	@Description	Requires permission: assignment:manage
//	@Description	Ends the driver's current stint. Succeeds also when the driver is on no truck.
//	@Tags			assignments
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Driver ID"	format(uuid)
//	@Success		204	"no content"
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403	{object}	apierror.Response	"missing permission assignment:manage"
//	@Failure		404	{object}	apierror.Response	"driver not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/{id}/truck [delete]
func (h *AssignmentHandler) Unassign(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.svc.UnassignDriver(c.Request.Context(), p, id); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// UnassignMe handles DELETE /api/v1/drivers/me/truck.
//
//	@Summary		Leave my truck
//	@Description	For the driver app: ends the caller's current stint. Needs a login, no extra permission. Succeeds also when on no truck.
//	@Tags			assignments
//	@Security		BearerAuth
//	@Success		204	"no content"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		404	{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/me/truck [delete]
func (h *AssignmentHandler) UnassignMe(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	if err := h.svc.UnassignMe(c.Request.Context(), p); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// bindAssignDriver reads the body; truck_id is required. The role is checked
// by the service (one place for the rule).
func bindAssignDriver(c *gin.Context) (service.AssignDriverInput, bool) {
	var req AssignDriverRequest
	if !bindJSON(c, &req) {
		return service.AssignDriverInput{}, false
	}
	if req.TruckID == nil {
		handleError(c, &validation.ValidationError{Fields: map[string]string{"truck_id": "is required"}})
		return service.AssignDriverInput{}, false
	}
	return service.AssignDriverInput{TruckID: *req.TruckID, Role: model.AssignmentRole(req.Role)}, true
}
