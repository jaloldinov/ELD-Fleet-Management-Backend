package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeDeviceService struct {
	got       auth.Device
	self      *service.DeviceSelf
	gotIngest []service.TelemetryInput
	result    *service.IngestResult
	err       error
}

func (f *fakeDeviceService) Ingest(_ context.Context, d auth.Device, in []service.TelemetryInput) (*service.IngestResult, error) {
	f.got, f.gotIngest = d, in
	return f.result, f.err
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
	r.POST("/api/v1/device/telemetry", h.Telemetry)
	r.POST("/api/v1/device/heartbeat", h.Heartbeat)
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

func TestDeviceHandler_Telemetry(t *testing.T) {
	seq := int64(105)
	svc := &fakeDeviceService{result: &service.IngestResult{Accepted: 1, Duplicates: 1,
		Rejected: []service.RejectedPoint{{Index: 2, Seq: &seq, Reason: "latitude: must be between -90 and 90"}}}}

	rec := do(deviceTestRouter(svc, true), http.MethodPost, "/api/v1/device/telemetry", `{"events":[
		{"seq":103,"recorded_at":"2026-11-08T14:30:05Z","latitude":32.7767,"longitude":-96.797,
		 "speed_mph":61.5,"odometer_mi":120345.2,"engine_hours":8123.4,"engine_on":true},
		{"seq":104,"recorded_at":"2026-11-08T14:30:10Z","speed_mph":0,"odometer_mi":120345.3,"engine_hours":8123.4,"engine_on":false},
		{"seq":105}
	]}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testDevice, svc.got)
	require.Len(t, svc.gotIngest, 3)
	assert.Equal(t, int64(103), *svc.gotIngest[0].Seq)
	assert.Equal(t, 61.5, *svc.gotIngest[0].SpeedMPH)
	assert.Nil(t, svc.gotIngest[1].Latitude, "no GPS fix")
	assert.Nil(t, svc.gotIngest[2].SpeedMPH, "missing fields reach the service, which rejects that point alone")
	body := rec.Body.String()
	assert.Contains(t, body, `"accepted":1`)
	assert.Contains(t, body, `"duplicates":1`)
	assert.Contains(t, body, `"rejected":[{"index":2,"seq":105,"reason":"latitude: must be between -90 and 90"}]`)
}

func TestDeviceHandler_Telemetry_NothingRejectedIsAnEmptyList(t *testing.T) {
	svc := &fakeDeviceService{result: &service.IngestResult{Accepted: 1}}

	rec := do(deviceTestRouter(svc, true), http.MethodPost, "/api/v1/device/telemetry", `{"events":[{"seq":1}]}`)

	assert.Contains(t, rec.Body.String(), `"rejected":[]`)
}

func TestDeviceHandler_Telemetry_MalformedBody(t *testing.T) {
	svc := &fakeDeviceService{}

	rec := do(deviceTestRouter(svc, true), http.MethodPost, "/api/v1/device/telemetry", `{"events":[{"seq":"one"}]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "broken JSON is a bug in the device, not a reading")
	assert.Nil(t, svc.gotIngest)
}

func TestDeviceHandler_Heartbeat(t *testing.T) {
	rec := do(deviceTestRouter(&fakeDeviceService{}, true), http.MethodPost, "/api/v1/device/heartbeat", "")
	assert.Equal(t, http.StatusNoContent, rec.Code, "the contact itself is the point")
}
