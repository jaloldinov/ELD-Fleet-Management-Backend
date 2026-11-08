package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// DeviceService is what DeviceHandler needs: the device-facing side.
type DeviceService interface {
	Me(ctx context.Context, d auth.Device) (*service.DeviceSelf, error)
	Ingest(ctx context.Context, d auth.Device, in []service.TelemetryInput) (*service.IngestResult, error)
}

// TelemetryRequest is a batch of points from one device, oldest first is
// customary but not required (each point carries its own seq and time).
type TelemetryRequest struct {
	Events []TelemetryEventRequest `json:"events"`
}

// TelemetryEventRequest is one reading. Required: seq, recorded_at,
// speed_mph, odometer_mi, engine_hours, engine_on. latitude/longitude
// together, or neither when there is no GPS fix.
type TelemetryEventRequest struct {
	Seq         *int64     `json:"seq" example:"101"`
	RecordedAt  *time.Time `json:"recorded_at" example:"2026-11-08T14:30:05Z"`
	Latitude    *float64   `json:"latitude,omitempty" example:"32.7767"`
	Longitude   *float64   `json:"longitude,omitempty" example:"-96.797"`
	SpeedMPH    *float64   `json:"speed_mph" example:"61.5"`
	OdometerMi  *float64   `json:"odometer_mi" example:"120345.2"`
	EngineHours *float64   `json:"engine_hours" example:"8123.4"`
	EngineOn    *bool      `json:"engine_on" example:"true"`
}

// TelemetryResponse: a final answer for every point of the batch. The
// device may drop all of them from its buffer.
type TelemetryResponse struct {
	Accepted   int                     `json:"accepted" example:"12"`  // new, stored now
	Duplicates int                     `json:"duplicates" example:"3"` // stored before (resent)
	Rejected   []RejectedPointResponse `json:"rejected"`               // never will be stored
}

type RejectedPointResponse struct {
	Index  int    `json:"index" example:"4"` // position in events
	Seq    *int64 `json:"seq" example:"105"`
	Reason string `json:"reason" example:"latitude: must be between -90 and 90"`
}

// DeviceSelfResponse: who the calling device is, and its truck.
type DeviceSelfResponse struct {
	DeviceID     uuid.UUID           `json:"device_id" swaggertype:"string" format:"uuid"`
	CompanyID    uuid.UUID           `json:"company_id" swaggertype:"string" format:"uuid"`
	SerialNumber string              `json:"serial_number" example:"PT30-0001"`
	Status       string              `json:"status" enums:"active,faulty,retired" example:"active"`
	Truck        *BoardTruckResponse `json:"truck"` // null = not installed
	// When it was installed in that truck: readings recorded earlier
	// belong to no truck (the device was not in it yet).
	InstalledAt *time.Time `json:"installed_at" example:"2026-11-08T07:30:00Z"`
}

// DeviceHandler serves the ELD devices themselves (/api/v1/device/...),
// authenticated by DeviceAuth with a device key, never a user token.
type DeviceHandler struct {
	svc DeviceService
}

func NewDeviceHandler(svc DeviceService) *DeviceHandler {
	return &DeviceHandler{svc: svc}
}

// Me handles GET /api/v1/device/me.
//
//	@Summary		Who am I (device)
//	@Description	For ELD devices: the calling device and the truck it is installed in. Authorization: Device <key>. Not behind the billing gate (logging hours of service is required by law).
//	@Tags			device
//	@Produce		json
//	@Security		DeviceKey
//	@Success		200	{object}	DeviceSelfResponse
//	@Failure		401	{object}	apierror.Response	"missing, unknown or revoked device key"
//	@Failure		403	{object}	apierror.Response	"the device is retired"
//	@Failure		500	{object}	apierror.Response
//	@Router			/api/v1/device/me [get]
func (h *DeviceHandler) Me(c *gin.Context) {
	d, ok := auth.DeviceFromContext(c.Request.Context())
	if !ok {
		// DeviceAuth always runs first; reaching here is a wiring bug.
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, "device authentication required", nil)
		return
	}
	self, err := h.svc.Me(c.Request.Context(), d)
	if err != nil {
		handleError(c, err)
		return
	}
	r := DeviceSelfResponse{DeviceID: self.Device.ID, CompanyID: self.Device.CompanyID,
		SerialNumber: self.Device.SerialNumber, Status: string(self.Device.Status)}
	if t := self.Truck; t != nil {
		r.Truck = &BoardTruckResponse{ID: t.ID, UnitNumber: t.UnitNumber, IsActive: t.IsActive}
		r.InstalledAt = utcOrNil(self.InstalledAt)
	}
	c.JSON(http.StatusOK, r)
}

// Telemetry handles POST /api/v1/device/telemetry.
//
//	@Summary		Send readings (device)
//	@Description	For ELD devices: a batch of up to 500 readings (also the way to sync after being offline). Every point gets a final answer: accepted, duplicate (its seq was stored before; resending is safe) or rejected with a reason. Each point is filed under the truck the device was installed in at its recorded_at. Authorization: Device <key>. Not behind the billing gate.
//	@Tags			device
//	@Accept			json
//	@Produce		json
//	@Security		DeviceKey
//	@Param			body	body		TelemetryRequest	true	"Readings"
//	@Success		200		{object}	TelemetryResponse
//	@Failure		400		{object}	apierror.Response	"no points, more than 500, or malformed JSON"
//	@Failure		401		{object}	apierror.Response	"missing, unknown or revoked device key"
//	@Failure		403		{object}	apierror.Response	"the device is retired"
//	@Failure		413		{object}	apierror.Response	"body too large"
//	@Failure		500		{object}	apierror.Response
//	@Router			/api/v1/device/telemetry [post]
func (h *DeviceHandler) Telemetry(c *gin.Context) {
	d, ok := auth.DeviceFromContext(c.Request.Context())
	if !ok {
		apierror.Respond(c, http.StatusUnauthorized, apierror.CodeUnauthorized, "device authentication required", nil)
		return
	}
	var req TelemetryRequest
	if !bindJSON(c, &req) {
		return
	}
	in := make([]service.TelemetryInput, len(req.Events))
	for i, e := range req.Events {
		in[i] = service.TelemetryInput{Seq: e.Seq, RecordedAt: e.RecordedAt, Latitude: e.Latitude, Longitude: e.Longitude,
			SpeedMPH: e.SpeedMPH, OdometerMi: e.OdometerMi, EngineHours: e.EngineHours, EngineOn: e.EngineOn}
	}

	res, err := h.svc.Ingest(c.Request.Context(), d, in)
	if err != nil {
		handleError(c, err)
		return
	}
	out := TelemetryResponse{Accepted: res.Accepted, Duplicates: res.Duplicates,
		Rejected: make([]RejectedPointResponse, 0, len(res.Rejected))} // [] not null
	for _, r := range res.Rejected {
		out.Rejected = append(out.Rejected, RejectedPointResponse{Index: r.Index, Seq: r.Seq, Reason: r.Reason})
	}
	c.JSON(http.StatusOK, out)
}

// Heartbeat handles POST /api/v1/device/heartbeat.
//
//	@Summary		I am alive (device)
//	@Description	For ELD devices with nothing new to report (parked, engine off): keeps the device "online". Any authenticated device request counts as contact; this one does nothing else. Authorization: Device <key>.
//	@Tags			device
//	@Security		DeviceKey
//	@Success		204	"no content"
//	@Failure		401	{object}	apierror.Response	"missing, unknown or revoked device key"
//	@Failure		403	{object}	apierror.Response	"the device is retired"
//	@Router			/api/v1/device/heartbeat [post]
func (h *DeviceHandler) Heartbeat(c *gin.Context) {
	// DeviceAuth already recorded the contact (last_seen_at).
	c.Status(http.StatusNoContent)
}
