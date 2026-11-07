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
	InstallDevice(ctx context.Context, p auth.Principal, deviceID, truckID uuid.UUID, replace bool) (*model.ELDDevice, error)
	RemoveDevice(ctx context.Context, p auth.Principal, deviceID uuid.UUID) (*model.ELDDevice, error)
	TruckBoard(ctx context.Context, p auth.Principal, truckID uuid.UUID) (*model.TruckBoard, error)
	DriverTruck(ctx context.Context, p auth.Principal, driverID uuid.UUID) (*model.DriverTruck, error)
	MyTruck(ctx context.Context, p auth.Principal) (*model.DriverTruck, error)
}

// ---------- DTOs ----------

type AssignDriverRequest struct {
	TruckID *uuid.UUID `json:"truck_id" swaggertype:"string" format:"uuid"`
	// Omit for primary. A truck has at most one of each.
	Role string `json:"role,omitempty" enums:"primary,co_driver" example:"primary"`
}

type AssignTruckRequest struct {
	TruckID *uuid.UUID `json:"truck_id" swaggertype:"string" format:"uuid"`
	// true: if the truck already has another device, take it out (e.g. it
	// broke and this one replaces it). false/omitted: 409 in that case.
	Replace bool `json:"replace,omitempty" example:"false"`
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

// CrewMemberResponse is a driver on a truck now.
type CrewMemberResponse struct {
	DriverID     uuid.UUID `json:"driver_id" swaggertype:"string" format:"uuid"`
	FirstName    string    `json:"first_name" example:"Ali"`
	LastName     string    `json:"last_name" example:"Valiyev"`
	Role         string    `json:"role" enums:"primary,co_driver" example:"primary"`
	Since        time.Time `json:"since" example:"2026-11-07T08:00:00Z"`
	AssignmentID uuid.UUID `json:"assignment_id" swaggertype:"string" format:"uuid"`
}

// InstalledDeviceResponse is the ELD device in a truck now.
type InstalledDeviceResponse struct {
	ID           uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	SerialNumber string    `json:"serial_number" example:"PT30-0001"`
	Since        time.Time `json:"since" example:"2026-11-07T07:30:00Z"`
	AssignmentID uuid.UUID `json:"assignment_id" swaggertype:"string" format:"uuid"`
}

type BoardTruckResponse struct {
	ID         uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	UnitNumber string    `json:"unit_number" example:"101"`
	IsActive   bool      `json:"is_active" example:"true"`
}

// TruckBoardResponse: who drives the truck now (primary first, at most two;
// [] = nobody) and which device is installed (null = none).
type TruckBoardResponse struct {
	Truck     BoardTruckResponse       `json:"truck"`
	Drivers   []CrewMemberResponse     `json:"drivers"`
	ELDDevice *InstalledDeviceResponse `json:"eld_device"`
}

// DriverTruckResponse: where a driver is now. role, since and truck are
// null when the driver is on no truck.
type DriverTruckResponse struct {
	DriverID uuid.UUID           `json:"driver_id" swaggertype:"string" format:"uuid"`
	Role     *string             `json:"role" enums:"primary,co_driver" example:"primary"`
	Since    *time.Time          `json:"since" example:"2026-11-07T08:00:00Z"`
	Truck    *TruckBoardResponse `json:"truck"`
}

func newTruckBoardResponse(b *model.TruckBoard) TruckBoardResponse {
	r := TruckBoardResponse{
		Truck:   BoardTruckResponse{ID: b.Truck.ID, UnitNumber: b.Truck.UnitNumber, IsActive: b.Truck.IsActive},
		Drivers: make([]CrewMemberResponse, 0, len(b.Crew)), // [] not null: "nobody" is a list
	}
	for _, m := range b.Crew {
		r.Drivers = append(r.Drivers, CrewMemberResponse{
			DriverID: m.Assignment.DriverID, FirstName: m.FirstName, LastName: m.LastName,
			Role: string(m.Assignment.Role), Since: m.Assignment.StartedAt.UTC(), AssignmentID: m.Assignment.ID,
		})
	}
	if d := b.Device; d != nil {
		r.ELDDevice = &InstalledDeviceResponse{ID: d.Assignment.ELDDeviceID, SerialNumber: d.SerialNumber,
			Since: d.Assignment.StartedAt.UTC(), AssignmentID: d.Assignment.ID}
	}
	return r
}

func newDriverTruckResponse(dt *model.DriverTruck) DriverTruckResponse {
	r := DriverTruckResponse{DriverID: dt.DriverID}
	if a := dt.Assignment; a != nil {
		role, since := string(a.Role), a.StartedAt.UTC()
		r.Role, r.Since = &role, &since
	}
	if dt.Board != nil {
		b := newTruckBoardResponse(dt.Board)
		r.Truck = &b
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

// InstallDevice handles PUT /api/v1/eld-devices/:id/truck.
//
//	@Summary		Install a device in a truck
//	@Description	Requires permission: eld:update
//	@Description	Installs the device in a truck of the same company, or moves it there from its current truck. With "replace": true, a device already in that truck is taken out first; without it, that is a 409.
//	@Tags			eld-devices
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string				true	"Device ID"	format(uuid)
//	@Param			body	body		AssignTruckRequest	true	"Truck to install the device in"
//	@Success		200		{object}	ELDDeviceResponse
//	@Failure		400		{object}	apierror.Response	"truck not found, out of service, or device inactive"
//	@Failure		401		{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402		{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403		{object}	apierror.Response	"missing permission eld:update"
//	@Failure		404		{object}	apierror.Response	"device not found"
//	@Failure		409		{object}	apierror.Response	"the truck already has a device (send replace: true to swap it)"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id}/truck [put]
func (h *AssignmentHandler) InstallDevice(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var req AssignTruckRequest
	if !bindJSON(c, &req) {
		return
	}
	if req.TruckID == nil {
		handleError(c, &validation.ValidationError{Fields: map[string]string{"truck_id": "is required"}})
		return
	}
	d, err := h.svc.InstallDevice(c.Request.Context(), p, id, *req.TruckID, req.Replace)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newELDDeviceResponse(d))
}

// RemoveDevice handles DELETE /api/v1/eld-devices/:id/truck.
//
//	@Summary		Remove a device from its truck
//	@Description	Requires permission: eld:update
//	@Description	Succeeds also when the device is not installed.
//	@Tags			eld-devices
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Device ID"	format(uuid)
//	@Success		200	{object}	ELDDeviceResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403	{object}	apierror.Response	"missing permission eld:update"
//	@Failure		404	{object}	apierror.Response
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/eld-devices/{id}/truck [delete]
func (h *AssignmentHandler) RemoveDevice(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	d, err := h.svc.RemoveDevice(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newELDDeviceResponse(d))
}

// TruckBoard handles GET /api/v1/trucks/:id/current.
//
//	@Summary		Who is in a truck now
//	@Description	Requires permission: assignment:read
//	@Description	The truck's drivers (primary first, at most two) and its ELD device, read as one consistent picture.
//	@Tags			assignments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Truck ID"	format(uuid)
//	@Success		200	{object}	TruckBoardResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403	{object}	apierror.Response	"missing permission assignment:read"
//	@Failure		404	{object}	apierror.Response	"truck not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/trucks/{id}/current [get]
func (h *AssignmentHandler) TruckBoard(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	b, err := h.svc.TruckBoard(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newTruckBoardResponse(b))
}

// DriverTruck handles GET /api/v1/drivers/:id/truck.
//
//	@Summary		Which truck a driver is on now
//	@Description	Requires permission: assignment:read
//	@Description	The driver's role and the board of their truck (co-driver, ELD device). role, since and truck are null when the driver is on no truck.
//	@Tags			assignments
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Driver ID"	format(uuid)
//	@Success		200	{object}	DriverTruckResponse
//	@Failure		400	{object}	apierror.Response	"invalid id"
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		403	{object}	apierror.Response	"missing permission assignment:read"
//	@Failure		404	{object}	apierror.Response	"driver not found"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/{id}/truck [get]
func (h *AssignmentHandler) DriverTruck(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	dt, err := h.svc.DriverTruck(c.Request.Context(), p, id)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverTruckResponse(dt))
}

// MyTruck handles GET /api/v1/drivers/me/truck.
//
//	@Summary		My truck now
//	@Description	For the driver app: the caller's truck, co-driver and the ELD device to connect to. Needs a login, no extra permission.
//	@Tags			assignments
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	DriverTruckResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		402	{object}	apierror.Response	"the company's subscription is not active"
//	@Failure		404	{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/me/truck [get]
func (h *AssignmentHandler) MyTruck(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	dt, err := h.svc.MyTruck(c.Request.Context(), p)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, newDriverTruckResponse(dt))
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
