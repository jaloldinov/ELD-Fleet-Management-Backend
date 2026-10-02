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
)

type fakeTruckService struct {
	gotPrincipal auth.Principal
	gotCreate    service.CreateTruckInput
	gotUpdate    service.UpdateTruckInput
	gotList      service.TruckListParams
	truck        *model.Truck
	err          error
}

func (f *fakeTruckService) Create(_ context.Context, p auth.Principal, in service.CreateTruckInput) (*model.Truck, error) {
	f.gotPrincipal, f.gotCreate = p, in
	return f.truck, f.err
}
func (f *fakeTruckService) Get(_ context.Context, p auth.Principal, _ uuid.UUID) (*model.Truck, error) {
	f.gotPrincipal = p
	return f.truck, f.err
}
func (f *fakeTruckService) List(_ context.Context, p auth.Principal, params service.TruckListParams) (*service.TruckPage, error) {
	f.gotPrincipal, f.gotList = p, params
	if f.err != nil {
		return nil, f.err
	}
	return &service.TruckPage{Items: []model.Truck{*f.truck}, Total: 1, Page: 1, PageSize: 20}, nil
}
func (f *fakeTruckService) Update(_ context.Context, p auth.Principal, _ uuid.UUID, in service.UpdateTruckInput) (*model.Truck, error) {
	f.gotPrincipal, f.gotUpdate = p, in
	return f.truck, f.err
}
func (f *fakeTruckService) Delete(_ context.Context, p auth.Principal, _ uuid.UUID) error {
	f.gotPrincipal = p
	return f.err
}

func sampleTruck() *model.Truck {
	year := 2022
	return &model.Truck{ID: testUserID, CompanyID: testCompanyID, UnitNumber: "101",
		VIN: "1M8GDM9AXKP042788", Make: "Freightliner", Year: &year, IsActive: true,
		CreatedAt: time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC)}
}

func truckRouter(svc TruckService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewTruckHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/trucks")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.PATCH("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	return r
}

func TestTruckHandler_Create(t *testing.T) {
	svc := &fakeTruckService{truck: sampleTruck()}

	rec := do(truckRouter(svc), http.MethodPost, "/api/v1/trucks",
		`{"unit_number":"101","vin":"1M8GDM9AXKP042788","year":2022}`)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.Equal(t, "1M8GDM9AXKP042788", svc.gotCreate.VIN)
	require.NotNil(t, svc.gotCreate.Year)
	assert.Equal(t, 2022, *svc.gotCreate.Year)
	assert.Contains(t, rec.Body.String(), `"license_plate":null`)
}

func TestTruckHandler_List_Filters(t *testing.T) {
	svc := &fakeTruckService{truck: sampleTruck()}

	rec := do(truckRouter(svc), http.MethodGet, "/api/v1/trucks?is_active=false&page=2", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, svc.gotList.IsActive)
	assert.False(t, *svc.gotList.IsActive)
	assert.Equal(t, 2, svc.gotList.Page)

	bad := do(truckRouter(svc), http.MethodGet, "/api/v1/trucks?is_active=maybe", "")
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	assert.Contains(t, decodeError(t, bad).Details, "is_active")
}

func TestTruckHandler_Update_RemovePlate(t *testing.T) {
	svc := &fakeTruckService{truck: sampleTruck()}

	do(truckRouter(svc), http.MethodPatch, "/api/v1/trucks/"+testUserID.String(), `{"license_plate":"","plate_state":""}`)

	require.NotNil(t, svc.gotUpdate.LicensePlate, `"" is sent, not omitted`)
	assert.Equal(t, "", *svc.gotUpdate.LicensePlate)
	assert.Nil(t, svc.gotUpdate.IsActive)
}

func TestTruckHandler_Errors(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		want   int
	}{
		{"VIN taken", http.MethodPost, "/api/v1/trucks", `{}`, model.ErrVINTaken, http.StatusConflict},
		{"unit taken", http.MethodPatch, "/api/v1/trucks/" + testUserID.String(), `{"unit_number":"1"}`, model.ErrUnitNumberTaken, http.StatusConflict},
		{"other tenant", http.MethodGet, "/api/v1/trucks/" + testUserID.String(), "", model.ErrNotFound, http.StatusNotFound},
		{"delete ok", http.MethodDelete, "/api/v1/trucks/" + testUserID.String(), "", nil, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(truckRouter(&fakeTruckService{truck: sampleTruck(), err: tt.err}), tt.method, tt.path, tt.body)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}
