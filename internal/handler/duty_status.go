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

// DutyStatusService is what DutyStatusHandler needs from the service layer.
type DutyStatusService interface {
	MyStatus(ctx context.Context, p auth.Principal) (*model.DutyStatusEvent, error)
	ChangeMyStatus(ctx context.Context, p auth.Principal, in service.ChangeDutyStatusInput) (*model.DutyStatusEvent, bool, error)
}

// ---------- DTOs ----------

type ChangeDutyStatusRequest struct {
	// driving is not accepted: the ELD records it from the truck's movement.
	Status string `json:"status" enums:"off_duty,sleeper_berth,on_duty" example:"on_duty"`
	// personal_conveyance only with off_duty, yard_moves only with on_duty;
	// both need a truck.
	Special *string `json:"special,omitempty" enums:"personal_conveyance,yard_moves"`
	Note    *string `json:"note,omitempty" example:"pre-trip inspection" maxLength:"500"`
}

// DutyStatusEventResponse is one logbook record. The status lasts from
// started_at until the driver's next record starts. truck_id and the
// readings are null when unknown (no truck, no ELD reading).
type DutyStatusEventResponse struct {
	ID          uuid.UUID  `json:"id" swaggertype:"string" format:"uuid"`
	DriverID    uuid.UUID  `json:"driver_id" swaggertype:"string" format:"uuid"`
	Status      string     `json:"status" enums:"off_duty,sleeper_berth,driving,on_duty" example:"on_duty"`
	Special     *string    `json:"special" enums:"personal_conveyance,yard_moves"`
	StartedAt   time.Time  `json:"started_at" example:"2026-11-11T06:00:00Z"`
	Origin      string     `json:"origin" enums:"auto,driver" example:"driver"`
	TruckID     *uuid.UUID `json:"truck_id" swaggertype:"string" format:"uuid"`
	Latitude    *float64   `json:"latitude" example:"32.7767"`
	Longitude   *float64   `json:"longitude" example:"-96.797"`
	OdometerMi  *float64   `json:"odometer_mi" example:"120500.5"`
	EngineHours *float64   `json:"engine_hours" example:"8012.3"`
	Note        *string    `json:"note"`
	CreatedAt   time.Time  `json:"created_at"`
}

// MyDutyStatusResponse: current is null when the driver has never
// recorded a status.
type MyDutyStatusResponse struct {
	Current *DutyStatusEventResponse `json:"current"`
}

func newDutyStatusEventResponse(e *model.DutyStatusEvent) DutyStatusEventResponse {
	r := DutyStatusEventResponse{
		ID: e.ID, DriverID: e.DriverID, Status: string(e.Status), StartedAt: e.StartedAt,
		Origin: string(e.Origin), TruckID: e.TruckID, Latitude: e.Latitude, Longitude: e.Longitude,
		OdometerMi: e.OdometerMi, EngineHours: e.EngineHours, Note: e.Note, CreatedAt: e.CreatedAt,
	}
	if e.Special != nil {
		s := string(*e.Special)
		r.Special = &s
	}
	return r
}

// ---------- Handler ----------

type DutyStatusHandler struct {
	svc DutyStatusService
}

func NewDutyStatusHandler(svc DutyStatusService) *DutyStatusHandler {
	return &DutyStatusHandler{svc: svc}
}

// MyStatus handles GET /api/v1/drivers/me/duty-status.
//
//	@Summary		My duty status now
//	@Description	For the driver app: the caller's status in force now (null if none recorded yet). Needs a login, no extra permission. Not behind the billing gate: hours of service must be logged by law.
//	@Tags			logbook
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	MyDutyStatusResponse
//	@Failure		401	{object}	apierror.Response	"missing or invalid access token"
//	@Failure		404	{object}	apierror.Response	"the caller is not a driver"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/drivers/me/duty-status [get]
func (h *DutyStatusHandler) MyStatus(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	e, err := h.svc.MyStatus(c.Request.Context(), p)
	if err != nil {
		handleError(c, err)
		return
	}
	var res MyDutyStatusResponse
	if e != nil {
		r := newDutyStatusEventResponse(e)
		res.Current = &r
	}
	c.JSON(http.StatusOK, res)
}

// ChangeMyStatus handles POST /api/v1/drivers/me/duty-status.
//
//	@Summary		Change my duty status
//	@Description	The driver presses a status button; the change starts NOW (server clock). Truck, position, odometer and engine hours are taken from the driver's truck and its ELD. Pressing the status already in force changes nothing (200 with that record); a real change is 201. Needs a login, no extra permission. Not behind the billing gate.
//	@Tags			logbook
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		ChangeDutyStatusRequest	true	"New status"
//	@Success		201		{object}	DutyStatusEventResponse	"recorded"
//	@Success		200		{object}	DutyStatusEventResponse	"already in that status"
//	@Failure		400		{object}	apierror.Response		"invalid status, special or note"
//	@Failure		401		{object}	apierror.Response		"missing or invalid access token"
//	@Failure		404		{object}	apierror.Response		"the caller is not a driver"
//	@Failure		409		{object}	apierror.Response		"another change in the same second"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/drivers/me/duty-status [post]
func (h *DutyStatusHandler) ChangeMyStatus(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var req ChangeDutyStatusRequest
	if !bindJSON(c, &req) {
		return
	}
	in := service.ChangeDutyStatusInput{Status: model.DutyStatus(req.Status), Note: req.Note}
	if req.Special != nil {
		s := model.SpecialDriving(*req.Special)
		in.Special = &s
	}
	e, created, err := h.svc.ChangeMyStatus(c.Request.Context(), p, in)
	if err != nil {
		handleError(c, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	c.JSON(code, newDutyStatusEventResponse(e))
}
