package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

type fakeELDService struct {
	gotPrincipal auth.Principal
	gotCreate    service.CreateELDDeviceInput
	gotUpdate    service.UpdateELDDeviceInput
	gotList      service.ELDDeviceListParams
	device       *model.ELDDevice
	err          error
}

func (f *fakeELDService) Create(_ context.Context, p auth.Principal, in service.CreateELDDeviceInput) (*model.ELDDevice, error) {
	f.gotPrincipal, f.gotCreate = p, in
	return f.device, f.err
}
func (f *fakeELDService) Get(_ context.Context, p auth.Principal, _ uuid.UUID) (*model.ELDDevice, error) {
	f.gotPrincipal = p
	return f.device, f.err
}
func (f *fakeELDService) List(_ context.Context, p auth.Principal, params service.ELDDeviceListParams) (*service.ELDDevicePage, error) {
	f.gotPrincipal, f.gotList = p, params
	if f.err != nil {
		return nil, f.err
	}
	return &service.ELDDevicePage{Items: []model.ELDDevice{*f.device}, Total: 1, Page: 1, PageSize: 20}, nil
}
func (f *fakeELDService) Update(_ context.Context, p auth.Principal, _ uuid.UUID, in service.UpdateELDDeviceInput) (*model.ELDDevice, error) {
	f.gotPrincipal, f.gotUpdate = p, in
	return f.device, f.err
}
func (f *fakeELDService) Delete(_ context.Context, p auth.Principal, _ uuid.UUID) error {
	f.gotPrincipal = p
	return f.err
}

func sampleELD() *model.ELDDevice {
	return &model.ELDDevice{ID: testUserID, CompanyID: testCompanyID, SerialNumber: "PT30-0001",
		Model: "PT30", FirmwareVersion: "1.2.3", IsActive: true,
		CreatedAt: time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)}
}

func eldRouter(svc ELDDeviceService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewELDDeviceHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/eld-devices")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.PATCH("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	return r
}

const eldPath = "/api/v1/eld-devices/"

func TestELDDeviceHandler_Create(t *testing.T) {
	svc := &fakeELDService{device: sampleELD()}

	rec := do(eldRouter(svc), http.MethodPost, "/api/v1/eld-devices",
		`{"serial_number":"PT30-0001","model":"PT30","firmware_version":"1.2.3"}`)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.Equal(t, "PT30-0001", svc.gotCreate.SerialNumber)
	assert.Equal(t, "/api/v1/eld-devices/"+testUserID.String(), rec.Header().Get("Location"))
	assert.Contains(t, rec.Body.String(), `"truck_id":null`, "not installed is an explicit null")
}

func TestELDDeviceHandler_List_Filters(t *testing.T) {
	svc := &fakeELDService{device: sampleELD()}
	truckID := uuid.New()

	rec := do(eldRouter(svc), http.MethodGet,
		"/api/v1/eld-devices?is_active=true&truck_id="+truckID.String(), "")

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.gotList.TruckID)
	assert.Equal(t, truckID, *svc.gotList.TruckID)
	assert.True(t, *svc.gotList.IsActive)
	assert.Nil(t, svc.gotList.CompanyID)
}

func TestELDDeviceHandler_List_BadQuery(t *testing.T) {
	rec := do(eldRouter(&fakeELDService{}), http.MethodGet, "/api/v1/eld-devices?truck_id=nope&is_active=maybe", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	details := decodeError(t, rec).Details
	assert.Contains(t, details, "truck_id")
	assert.Contains(t, details, "is_active", "all problems reported at once")
}

func TestELDDeviceHandler_Update(t *testing.T) {
	svc := &fakeELDService{device: sampleELD()}

	rec := do(eldRouter(svc), http.MethodPatch, eldPath+testUserID.String(), `{"firmware_version":"2.0.0"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.gotUpdate.FirmwareVersion)
	assert.Equal(t, "2.0.0", *svc.gotUpdate.FirmwareVersion)
	assert.Nil(t, svc.gotUpdate.IsActive, "omitted field stays nil")
}

func TestELDDeviceHandler_Errors(t *testing.T) {
	id := testUserID.String()
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		want   int
	}{
		{"serial taken", http.MethodPost, "/api/v1/eld-devices", `{}`, model.ErrSerialTaken, http.StatusConflict},
		{"other tenant", http.MethodGet, eldPath + id, "", model.ErrNotFound, http.StatusNotFound},
		{"validation", http.MethodPatch, eldPath + id, `{"is_active":false}`,
			&validation.ValidationError{Fields: map[string]string{"is_active": "x"}}, http.StatusBadRequest},
		{"bad id", http.MethodDelete, eldPath + "xyz", "", nil, http.StatusBadRequest},
		{"delete ok", http.MethodDelete, eldPath + id, "", nil, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(eldRouter(&fakeELDService{device: sampleELD(), err: tt.err}), tt.method, tt.path, tt.body)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestELDDeviceHandler_Status(t *testing.T) {
	d := sampleELD()
	d.Status = model.ELDFaulty
	svc := &fakeELDService{device: d}

	rec := do(eldRouter(svc), http.MethodPatch, eldPath+testUserID.String(), `{"status":"faulty"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.gotUpdate.Status)
	assert.Equal(t, model.ELDFaulty, *svc.gotUpdate.Status)
	assert.Contains(t, rec.Body.String(), `"status":"faulty"`)

	rec = do(eldRouter(svc), http.MethodGet, "/api/v1/eld-devices?status=retired", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, model.ELDRetired, *svc.gotList.Status)

	rec = do(eldRouter(svc), http.MethodGet, "/api/v1/eld-devices?status=lost", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
