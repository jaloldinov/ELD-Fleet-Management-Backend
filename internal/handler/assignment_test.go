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

type fakeAssignmentService struct {
	gotPrincipal auth.Principal
	gotDriverID  uuid.UUID
	gotInput     service.AssignDriverInput
	gotDeviceID  uuid.UUID
	gotTruckID   uuid.UUID
	gotReplace   bool
	calls        []string
	assignment   *model.DriverAssignment
	device       *model.ELDDevice
	board        *model.TruckBoard
	driverTruck  *model.DriverTruck
	gotHistory   service.AssignmentHistoryParams
	driverPage   *service.DriverAssignmentPage
	eldPage      *service.ELDAssignmentPage
	err          error
}

func (f *fakeAssignmentService) AssignDriver(_ context.Context, p auth.Principal, driverID uuid.UUID, in service.AssignDriverInput) (*model.DriverAssignment, error) {
	f.gotPrincipal, f.gotDriverID, f.gotInput = p, driverID, in
	f.calls = append(f.calls, "AssignDriver")
	return f.assignment, f.err
}
func (f *fakeAssignmentService) AssignMe(_ context.Context, p auth.Principal, in service.AssignDriverInput) (*model.DriverAssignment, error) {
	f.gotPrincipal, f.gotInput = p, in
	f.calls = append(f.calls, "AssignMe")
	return f.assignment, f.err
}
func (f *fakeAssignmentService) UnassignDriver(_ context.Context, p auth.Principal, driverID uuid.UUID) error {
	f.gotPrincipal, f.gotDriverID = p, driverID
	f.calls = append(f.calls, "UnassignDriver")
	return f.err
}
func (f *fakeAssignmentService) UnassignMe(_ context.Context, p auth.Principal) error {
	f.gotPrincipal = p
	f.calls = append(f.calls, "UnassignMe")
	return f.err
}

func (f *fakeAssignmentService) InstallDevice(_ context.Context, p auth.Principal, deviceID, truckID uuid.UUID, replace bool) (*model.ELDDevice, error) {
	f.gotPrincipal, f.gotDeviceID, f.gotTruckID, f.gotReplace = p, deviceID, truckID, replace
	f.calls = append(f.calls, "InstallDevice")
	return f.device, f.err
}
func (f *fakeAssignmentService) RemoveDevice(_ context.Context, p auth.Principal, deviceID uuid.UUID) (*model.ELDDevice, error) {
	f.gotPrincipal, f.gotDeviceID = p, deviceID
	f.calls = append(f.calls, "RemoveDevice")
	return f.device, f.err
}

func (f *fakeAssignmentService) TruckBoard(_ context.Context, p auth.Principal, truckID uuid.UUID) (*model.TruckBoard, error) {
	f.gotPrincipal, f.gotTruckID = p, truckID
	f.calls = append(f.calls, "TruckBoard")
	return f.board, f.err
}
func (f *fakeAssignmentService) DriverTruck(_ context.Context, p auth.Principal, driverID uuid.UUID) (*model.DriverTruck, error) {
	f.gotPrincipal, f.gotDriverID = p, driverID
	f.calls = append(f.calls, "DriverTruck")
	return f.driverTruck, f.err
}
func (f *fakeAssignmentService) MyTruck(_ context.Context, p auth.Principal) (*model.DriverTruck, error) {
	f.gotPrincipal = p
	f.calls = append(f.calls, "MyTruck")
	return f.driverTruck, f.err
}

func (f *fakeAssignmentService) DriverHistory(_ context.Context, p auth.Principal, params service.AssignmentHistoryParams) (*service.DriverAssignmentPage, error) {
	f.gotPrincipal, f.gotHistory = p, params
	f.calls = append(f.calls, "DriverHistory")
	if f.err != nil {
		return nil, f.err
	}
	return f.driverPage, nil
}
func (f *fakeAssignmentService) ELDHistory(_ context.Context, p auth.Principal, params service.AssignmentHistoryParams) (*service.ELDAssignmentPage, error) {
	f.gotPrincipal, f.gotHistory = p, params
	f.calls = append(f.calls, "ELDHistory")
	if f.err != nil {
		return nil, f.err
	}
	return f.eldPage, nil
}

func sampleAssignment() *model.DriverAssignment {
	return &model.DriverAssignment{ID: uuid.New(), CompanyID: testCompanyID, DriverID: uuid.New(), TruckID: uuid.New(),
		Role: model.AssignmentPrimary, StartedAt: time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC), StartedBy: &testUserID}
}

// assignmentRouter registers the routes like NewRouter: "me" next to ":id".
func assignmentRouter(svc AssignmentService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAssignmentHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/drivers")
	g.GET("/me/truck", h.MyTruck)
	g.GET("/:id/truck", h.DriverTruck)
	g.PUT("/me/truck", h.AssignMe)
	g.DELETE("/me/truck", h.UnassignMe)
	g.PUT("/:id/truck", h.Assign)
	g.DELETE("/:id/truck", h.Unassign)
	r.GET("/api/v1/trucks/:id/current", h.TruckBoard)
	r.GET("/api/v1/driver-assignments", h.ListDriverAssignments)
	r.GET("/api/v1/eld-assignments", h.ListELDAssignments)
	e := r.Group("/api/v1/eld-devices")
	e.PUT("/:id/truck", h.InstallDevice)
	e.DELETE("/:id/truck", h.RemoveDevice)
	return r
}

func TestAssignmentHandler_Assign(t *testing.T) {
	svc := &fakeAssignmentService{assignment: sampleAssignment()}
	driverID, truckID := uuid.New(), uuid.New()

	rec := do(assignmentRouter(svc), http.MethodPut, "/api/v1/drivers/"+driverID.String()+"/truck",
		`{"truck_id":"`+truckID.String()+`","role":"co_driver"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.Equal(t, driverID, svc.gotDriverID)
	assert.Equal(t, service.AssignDriverInput{TruckID: truckID, Role: model.AssignmentCoDriver}, svc.gotInput)
	body := rec.Body.String()
	assert.Contains(t, body, `"role":"primary"`)
	assert.Contains(t, body, `"ended_at":null`, "current stint: explicit null")
	assert.Contains(t, body, `"started_at":"2026-11-07T08:00:00Z"`)
}

func TestAssignmentHandler_Assign_RoleOptional(t *testing.T) {
	svc := &fakeAssignmentService{assignment: sampleAssignment()}

	rec := do(assignmentRouter(svc), http.MethodPut, "/api/v1/drivers/"+uuid.NewString()+"/truck",
		`{"truck_id":"`+uuid.NewString()+`"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, svc.gotInput.Role, "the service picks the default")
}

func TestAssignmentHandler_Assign_MissingTruckID(t *testing.T) {
	svc := &fakeAssignmentService{}

	rec := do(assignmentRouter(svc), http.MethodPut, "/api/v1/drivers/"+uuid.NewString()+"/truck", `{"role":"primary"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, decodeError(t, rec).Details, "truck_id")
	assert.Empty(t, svc.calls)
}

func TestAssignmentHandler_MeRoutesAreNotMistakenForAnID(t *testing.T) {
	svc := &fakeAssignmentService{assignment: sampleAssignment()}
	r := assignmentRouter(svc)

	rec := do(r, http.MethodPut, "/api/v1/drivers/me/truck", `{"truck_id":"`+uuid.NewString()+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = do(r, http.MethodDelete, "/api/v1/drivers/me/truck", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)

	assert.Equal(t, []string{"AssignMe", "UnassignMe"}, svc.calls)
}

func TestAssignmentHandler_Unassign(t *testing.T) {
	svc := &fakeAssignmentService{}
	driverID := uuid.New()

	rec := do(assignmentRouter(svc), http.MethodDelete, "/api/v1/drivers/"+driverID.String()+"/truck", "")

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, []string{"UnassignDriver"}, svc.calls)
	assert.Equal(t, driverID, svc.gotDriverID)
}

func TestAssignmentHandler_Errors(t *testing.T) {
	id := uuid.NewString()
	body := `{"truck_id":"` + id + `"}`
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		want   int
	}{
		{"seat taken", http.MethodPut, "/api/v1/drivers/" + id + "/truck", body, model.ErrSeatTaken, http.StatusConflict},
		{"driver on truck", http.MethodPut, "/api/v1/drivers/" + id + "/truck", body, model.ErrDriverOnTruck, http.StatusConflict},
		{"not a driver", http.MethodPut, "/api/v1/drivers/me/truck", body, model.ErrNotFound, http.StatusNotFound},
		{"truck out of service", http.MethodPut, "/api/v1/drivers/" + id + "/truck", body,
			&validation.ValidationError{Fields: map[string]string{"truck_id": "x"}}, http.StatusBadRequest},
		{"bad id", http.MethodDelete, "/api/v1/drivers/xyz/truck", "", nil, http.StatusBadRequest},
		{"unknown field", http.MethodPut, "/api/v1/drivers/" + id + "/truck", `{"truck":"x"}`, nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(assignmentRouter(&fakeAssignmentService{assignment: sampleAssignment(), err: tt.err}), tt.method, tt.path, tt.body)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestAssignmentHandler_InstallDevice(t *testing.T) {
	truckID := uuid.New()
	installed := sampleELD()
	installed.TruckID = &truckID
	svc := &fakeAssignmentService{device: installed}

	rec := do(assignmentRouter(svc), http.MethodPut, eldPath+testUserID.String()+"/truck",
		`{"truck_id":"`+truckID.String()+`"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testUserID, svc.gotDeviceID)
	assert.Equal(t, truckID, svc.gotTruckID)
	assert.False(t, svc.gotReplace, "omitted = do not replace")
	assert.Contains(t, rec.Body.String(), `"truck_id":"`+truckID.String()+`"`)
}

func TestAssignmentHandler_InstallDevice_Replace(t *testing.T) {
	svc := &fakeAssignmentService{device: sampleELD()}

	rec := do(assignmentRouter(svc), http.MethodPut, eldPath+testUserID.String()+"/truck",
		`{"truck_id":"`+uuid.NewString()+`","replace":true}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, svc.gotReplace)
}

func TestAssignmentHandler_InstallDevice_MissingTruckID(t *testing.T) {
	svc := &fakeAssignmentService{}

	rec := do(assignmentRouter(svc), http.MethodPut, eldPath+testUserID.String()+"/truck", `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, decodeError(t, rec).Details, "truck_id")
	assert.Empty(t, svc.calls)
}

func TestAssignmentHandler_RemoveDevice(t *testing.T) {
	svc := &fakeAssignmentService{device: sampleELD()}

	rec := do(assignmentRouter(svc), http.MethodDelete, eldPath+testUserID.String()+"/truck", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"RemoveDevice"}, svc.calls)
	assert.Contains(t, rec.Body.String(), `"truck_id":null`)
}

func TestAssignmentHandler_InstallDevice_TruckHasELD(t *testing.T) {
	svc := &fakeAssignmentService{err: model.ErrTruckHasELD}

	rec := do(assignmentRouter(svc), http.MethodPut, eldPath+testUserID.String()+"/truck",
		`{"truck_id":"`+uuid.NewString()+`"}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func sampleBoard() *model.TruckBoard {
	since := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	return &model.TruckBoard{
		Truck: model.Truck{ID: uuid.New(), UnitNumber: "101", IsActive: true},
		Crew: []model.CrewMember{{
			Assignment: model.DriverAssignment{ID: uuid.New(), DriverID: uuid.New(), Role: model.AssignmentPrimary, StartedAt: since},
			FirstName:  "Ali", LastName: "Valiyev",
		}},
		Device: &model.InstalledDevice{
			Assignment:   model.ELDAssignment{ID: uuid.New(), ELDDeviceID: uuid.New(), StartedAt: since},
			SerialNumber: "PT30-0001",
		},
	}
}

func TestAssignmentHandler_TruckBoard(t *testing.T) {
	b := sampleBoard()
	svc := &fakeAssignmentService{board: b}

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/trucks/"+b.Truck.ID.String()+"/current", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, b.Truck.ID, svc.gotTruckID)
	body := rec.Body.String()
	assert.Contains(t, body, `"unit_number":"101"`)
	assert.Contains(t, body, `"first_name":"Ali"`)
	assert.Contains(t, body, `"role":"primary"`)
	assert.Contains(t, body, `"since":"2026-11-07T08:00:00Z"`)
	assert.Contains(t, body, `"serial_number":"PT30-0001"`)
}

func TestAssignmentHandler_TruckBoard_EmptyTruck(t *testing.T) {
	svc := &fakeAssignmentService{board: &model.TruckBoard{Truck: model.Truck{ID: uuid.New(), UnitNumber: "205"}}}

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/trucks/"+uuid.NewString()+"/current", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"drivers":[]`, "nobody is an empty list, not null")
	assert.Contains(t, body, `"eld_device":null`)
}

func TestAssignmentHandler_DriverTruck(t *testing.T) {
	b := sampleBoard()
	driverID := b.Crew[0].Assignment.DriverID
	a := b.Crew[0].Assignment
	svc := &fakeAssignmentService{driverTruck: &model.DriverTruck{DriverID: driverID, Assignment: &a, Board: b}}

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/drivers/"+driverID.String()+"/truck", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"DriverTruck"}, svc.calls)
	body := rec.Body.String()
	assert.Contains(t, body, `"driver_id":"`+driverID.String()+`"`)
	assert.Contains(t, body, `"truck":{"truck":{"id":"`+b.Truck.ID.String())
}

func TestAssignmentHandler_DriverTruck_OnNoTruck(t *testing.T) {
	svc := &fakeAssignmentService{driverTruck: &model.DriverTruck{DriverID: uuid.New()}}

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/drivers/me/truck", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"MyTruck"}, svc.calls, `"me" is not taken for an id`)
	body := rec.Body.String()
	assert.Contains(t, body, `"role":null`)
	assert.Contains(t, body, `"since":null`)
	assert.Contains(t, body, `"truck":null`)
}

func TestAssignmentHandler_Board_Errors(t *testing.T) {
	tests := []struct {
		name string
		path string
		err  error
		want int
	}{
		{"truck not found", "/api/v1/trucks/" + uuid.NewString() + "/current", model.ErrNotFound, http.StatusNotFound},
		{"bad truck id", "/api/v1/trucks/xyz/current", nil, http.StatusBadRequest},
		{"driver not found", "/api/v1/drivers/" + uuid.NewString() + "/truck", model.ErrNotFound, http.StatusNotFound},
		{"not a driver", "/api/v1/drivers/me/truck", model.ErrNotFound, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(assignmentRouter(&fakeAssignmentService{err: tt.err}), http.MethodGet, tt.path, "")
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestAssignmentHandler_ListDriverAssignments(t *testing.T) {
	a := sampleAssignment()
	svc := &fakeAssignmentService{driverPage: &service.DriverAssignmentPage{
		Items: []model.DriverAssignmentRecord{{Assignment: *a, TruckUnitNumber: "101", DriverFirstName: "Ali", DriverLastName: "Valiyev"}},
		Total: 1, Page: 1, PageSize: 20,
	}}
	driverID, truckID := uuid.New(), uuid.New()

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/driver-assignments?driver_id="+driverID.String()+
		"&truck_id="+truckID.String()+"&at=2026-11-03T14:30:00Z&page=2&page_size=5", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	got := svc.gotHistory
	assert.Equal(t, driverID, *got.DriverID)
	assert.Equal(t, truckID, *got.TruckID)
	assert.Equal(t, time.Date(2026, 11, 3, 14, 30, 0, 0, time.UTC), got.Period.At.UTC())
	assert.Nil(t, got.Period.From)
	assert.Equal(t, 2, got.Page)
	assert.Equal(t, 5, got.PageSize)

	body := rec.Body.String()
	assert.Contains(t, body, `"truck_unit_number":"101"`)
	assert.Contains(t, body, `"driver_first_name":"Ali"`)
	assert.Contains(t, body, `"role":"primary"`, "the stint's own fields sit at the top level")
	assert.Contains(t, body, `"ended_at":null`)
	assert.Contains(t, body, `"total":1`)
}

func TestAssignmentHandler_ListDriverAssignments_BadQuery(t *testing.T) {
	svc := &fakeAssignmentService{}

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/driver-assignments?driver_id=x&at=yesterday&page=one", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	details := decodeError(t, rec).Details
	assert.Contains(t, details, "driver_id")
	assert.Contains(t, details, "at")
	assert.Contains(t, details, "page", "all problems reported at once")
	assert.Empty(t, svc.calls)
}

func TestAssignmentHandler_ListELDAssignments(t *testing.T) {
	ended := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	svc := &fakeAssignmentService{eldPage: &service.ELDAssignmentPage{
		Items: []model.ELDAssignmentRecord{{
			Assignment:      model.ELDAssignment{ID: uuid.New(), ELDDeviceID: uuid.New(), TruckID: uuid.New(), EndedAt: &ended},
			TruckUnitNumber: "205", SerialNumber: "PT30-0009",
		}},
		Total: 1, Page: 1, PageSize: 20,
	}}
	deviceID := uuid.New()

	rec := do(assignmentRouter(svc), http.MethodGet, "/api/v1/eld-assignments?eld_device_id="+deviceID.String()+
		"&from=2026-11-01T00:00:00Z&to=2026-11-10T00:00:00Z", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, deviceID, *svc.gotHistory.ELDDeviceID)
	require.NotNil(t, svc.gotHistory.Period.From)
	require.NotNil(t, svc.gotHistory.Period.To)
	body := rec.Body.String()
	assert.Contains(t, body, `"serial_number":"PT30-0009"`)
	assert.Contains(t, body, `"truck_unit_number":"205"`)
	assert.Contains(t, body, `"ended_at":"2026-11-09T16:00:00Z"`)
}
