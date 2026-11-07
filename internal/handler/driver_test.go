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

type fakeDriverService struct {
	gotPrincipal auth.Principal
	gotCreate    service.CreateDriverInput
	gotUpdate    service.UpdateDriverInput
	gotList      service.DriverListParams
	dd           *model.DriverDetails
	page         *service.DriverPage
	err          error
	createCalled bool
}

func (f *fakeDriverService) Create(_ context.Context, p auth.Principal, in service.CreateDriverInput) (*model.DriverDetails, error) {
	f.createCalled, f.gotPrincipal, f.gotCreate = true, p, in
	return f.dd, f.err
}
func (f *fakeDriverService) Get(_ context.Context, p auth.Principal, _ uuid.UUID) (*model.DriverDetails, error) {
	f.gotPrincipal = p
	return f.dd, f.err
}
func (f *fakeDriverService) Me(_ context.Context, p auth.Principal) (*model.DriverDetails, error) {
	f.gotPrincipal = p
	return f.dd, f.err
}
func (f *fakeDriverService) List(_ context.Context, p auth.Principal, params service.DriverListParams) (*service.DriverPage, error) {
	f.gotPrincipal, f.gotList = p, params
	return f.page, f.err
}
func (f *fakeDriverService) Update(_ context.Context, p auth.Principal, _ uuid.UUID, in service.UpdateDriverInput) (*model.DriverDetails, error) {
	f.gotPrincipal, f.gotUpdate = p, in
	return f.dd, f.err
}
func (f *fakeDriverService) Delete(_ context.Context, p auth.Principal, _ uuid.UUID) error {
	f.gotPrincipal = p
	return f.err
}

func sampleDriver() *model.DriverDetails {
	username := "driver.bob"
	return &model.DriverDetails{
		Driver: model.Driver{
			ID: testUserID, UserID: uuid.New(), CompanyID: testCompanyID,
			LicenseNumber: "D1234567", LicenseState: "TX",
			LicenseExpiresAt: time.Date(2028, 5, 31, 0, 0, 0, 0, time.UTC),
			CreatedAt:        time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
		},
		User: model.User{Username: &username, FirstName: "Bob", LastName: "Lee", IsActive: true,
			PasswordHash: "$2a$12$SECRET-HASH-MUST-NOT-LEAK"},
	}
}

func driverRouter(svc DriverService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewDriverHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/drivers")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/me", h.Me)
	g.GET("/:id", h.Get)
	g.PATCH("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	return r
}

func TestDriverHandler_Create(t *testing.T) {
	svc := &fakeDriverService{dd: sampleDriver()}

	rec := do(driverRouter(svc), http.MethodPost, "/api/v1/drivers", `{
		"username": "driver.bob", "password": "correct horse", "first_name": "Bob", "last_name": "Lee",
		"license_number": "D1234567", "license_state": "TX", "license_expires_at": "2028-05-31"
	}`)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.Equal(t, time.Date(2028, 5, 31, 0, 0, 0, 0, time.UTC), svc.gotCreate.LicenseExpiresAt, "date parsed in the handler")
	assert.Contains(t, rec.Body.String(), `"license_expires_at":"2028-05-31"`, "date formatted back as YYYY-MM-DD")
	assert.NotContains(t, rec.Body.String(), "SECRET-HASH")
}

func TestDriverHandler_Create_BadDate(t *testing.T) {
	svc := &fakeDriverService{}

	rec := do(driverRouter(svc), http.MethodPost, "/api/v1/drivers", `{"license_expires_at":"05/31/2028"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, decodeError(t, rec).Details, "license_expires_at")
	assert.False(t, svc.createCalled, "service not called with an unparsable date")
}

func TestDriverHandler_MeIsRoutedBeforeID(t *testing.T) {
	svc := &fakeDriverService{dd: sampleDriver()}

	rec := do(driverRouter(svc), http.MethodGet, "/api/v1/drivers/me", "")

	assert.Equal(t, http.StatusOK, rec.Code, `"me" must not be parsed as an :id`)
}

func TestDriverHandler_Update_OptionalDate(t *testing.T) {
	svc := &fakeDriverService{dd: sampleDriver()}

	do(driverRouter(svc), http.MethodPatch, "/api/v1/drivers/"+testUserID.String(), `{"license_state":"OK"}`)

	require.NotNil(t, svc.gotUpdate.LicenseState)
	assert.Nil(t, svc.gotUpdate.LicenseExpiresAt, "omitted date stays nil")
}

func TestDriverHandler_List(t *testing.T) {
	svc := &fakeDriverService{page: &service.DriverPage{Items: []model.DriverDetails{*sampleDriver()}, Total: 1, Page: 1, PageSize: 20}}

	rec := do(driverRouter(svc), http.MethodGet, "/api/v1/drivers?company_id="+testCompanyID.String(), "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testCompanyID, *svc.gotList.CompanyID)
	assert.Contains(t, rec.Body.String(), `"total":1`)
}

func TestDriverHandler_Errors(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		want   int
	}{
		{"license taken", http.MethodPost, "/api/v1/drivers", `{}`, model.ErrLicenseTaken, http.StatusConflict},
		{"other tenant", http.MethodGet, "/api/v1/drivers/" + testUserID.String(), "", model.ErrNotFound, http.StatusNotFound},
		{"not a driver", http.MethodGet, "/api/v1/drivers/me", "", model.ErrNotFound, http.StatusNotFound},
		{"bad id", http.MethodDelete, "/api/v1/drivers/xyz", "", nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(driverRouter(&fakeDriverService{err: tt.err}), tt.method, tt.path, tt.body)
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

func TestDriverHandler_List_ByDispatcher(t *testing.T) {
	t.Run(`"me" is the caller`, func(t *testing.T) {
		svc := &fakeDriverService{page: onePage()}

		rec := do(driverRouter(svc), http.MethodGet, "/api/v1/drivers?dispatcher_id=me", "")

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, svc.gotList.DispatcherID)
		assert.Equal(t, testCaller.UserID, *svc.gotList.DispatcherID)
	})

	t.Run("a user id", func(t *testing.T) {
		svc := &fakeDriverService{page: onePage()}
		id := uuid.New()

		rec := do(driverRouter(svc), http.MethodGet, "/api/v1/drivers?dispatcher_id="+id.String(), "")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, id, *svc.gotList.DispatcherID)
	})

	t.Run("anything else", func(t *testing.T) {
		rec := do(driverRouter(&fakeDriverService{}), http.MethodGet, "/api/v1/drivers?dispatcher_id=you", "")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, decodeError(t, rec).Details, "dispatcher_id")
	})

	t.Run("no dispatcher is an explicit null", func(t *testing.T) {
		rec := do(driverRouter(&fakeDriverService{page: onePage()}), http.MethodGet, "/api/v1/drivers", "")
		assert.Contains(t, rec.Body.String(), `"dispatcher_id":null`)
	})
}

func onePage() *service.DriverPage {
	return &service.DriverPage{Items: []model.DriverDetails{*sampleDriver()}, Total: 1, Page: 1, PageSize: 20}
}
