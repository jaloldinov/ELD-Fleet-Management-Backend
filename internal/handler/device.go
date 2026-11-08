package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

// DeviceService is what DeviceHandler needs: the device-facing side.
type DeviceService interface {
	Me(ctx context.Context, d auth.Device) (*service.DeviceSelf, error)
}

// DeviceSelfResponse: who the calling device is, and its truck.
type DeviceSelfResponse struct {
	DeviceID     uuid.UUID           `json:"device_id" swaggertype:"string" format:"uuid"`
	CompanyID    uuid.UUID           `json:"company_id" swaggertype:"string" format:"uuid"`
	SerialNumber string              `json:"serial_number" example:"PT30-0001"`
	Status       string              `json:"status" enums:"active,faulty,retired" example:"active"`
	Truck        *BoardTruckResponse `json:"truck"` // null = not installed
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
	}
	c.JSON(http.StatusOK, r)
}
