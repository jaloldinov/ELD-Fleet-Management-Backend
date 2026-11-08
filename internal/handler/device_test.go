package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeDeviceService struct {
	got  auth.Device
	self *service.DeviceSelf
	err  error
}

func (f *fakeDeviceService) Me(_ context.Context, d auth.Device) (*service.DeviceSelf, error) {
	f.got = d
	return f.self, f.err
}

var testDevice = auth.Device{DeviceID: uuid.New(), CompanyID: uuid.New()}

// deviceTestRouter stands in for DeviceAuth: the device comes from the
// context, set by a tiny middleware (or not at all, withDevice=false).
func deviceTestRouter(svc DeviceService, withDevice bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewDeviceHandler(svc)
	r := gin.New()
	if withDevice {
		r.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithDevice(c.Request.Context(), testDevice))
		})
	}
	r.GET("/api/v1/device/me", h.Me)
	return r
}

func TestDeviceHandler_Me(t *testing.T) {
	d := sampleELD()
	tr := model.Truck{ID: uuid.New(), UnitNumber: "101", IsActive: true}
	svc := &fakeDeviceService{self: &service.DeviceSelf{Device: *d, Truck: &tr}}

	rec := do(deviceTestRouter(svc, true), http.MethodGet, "/api/v1/device/me", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testDevice, svc.got, "the device from the context, never from the request")
	body := rec.Body.String()
	assert.Contains(t, body, `"serial_number":"PT30-0001"`)
	assert.Contains(t, body, `"unit_number":"101"`)
}

func TestDeviceHandler_Me_NotInstalled(t *testing.T) {
	svc := &fakeDeviceService{self: &service.DeviceSelf{Device: *sampleELD()}}

	rec := do(deviceTestRouter(svc, true), http.MethodGet, "/api/v1/device/me", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"truck":null`)
}

func TestDeviceHandler_Me_NoDevice(t *testing.T) {
	rec := do(deviceTestRouter(&fakeDeviceService{}, false), http.MethodGet, "/api/v1/device/me", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "a wiring bug fails closed")
}
