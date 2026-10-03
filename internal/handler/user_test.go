package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// fakeUserService records what the handler passed and returns canned results.
type fakeUserService struct {
	gotPrincipal auth.Principal
	gotID        uuid.UUID
	gotCreate    service.CreateUserInput
	gotUpdate    service.UpdateUserInput
	gotList      service.UserListParams
	gotPassword  string

	details *service.UserDetails
	page    *service.UserPage
	profile *service.UserProfile
	err     error
}

func (f *fakeUserService) Create(_ context.Context, p auth.Principal, in service.CreateUserInput) (*service.UserDetails, error) {
	f.gotPrincipal, f.gotCreate = p, in
	return f.details, f.err
}

func (f *fakeUserService) Get(_ context.Context, p auth.Principal, id uuid.UUID) (*service.UserDetails, error) {
	f.gotPrincipal, f.gotID = p, id
	return f.details, f.err
}

func (f *fakeUserService) List(_ context.Context, p auth.Principal, params service.UserListParams) (*service.UserPage, error) {
	f.gotPrincipal, f.gotList = p, params
	return f.page, f.err
}

func (f *fakeUserService) Update(_ context.Context, p auth.Principal, id uuid.UUID, in service.UpdateUserInput) (*service.UserDetails, error) {
	f.gotPrincipal, f.gotID, f.gotUpdate = p, id, in
	return f.details, f.err
}

func (f *fakeUserService) Delete(_ context.Context, p auth.Principal, id uuid.UUID) error {
	f.gotPrincipal, f.gotID = p, id
	return f.err
}

func (f *fakeUserService) SetPassword(_ context.Context, p auth.Principal, id uuid.UUID, pw string) error {
	f.gotPrincipal, f.gotID, f.gotPassword = p, id, pw
	return f.err
}

func (f *fakeUserService) GetProfile(_ context.Context, id uuid.UUID) (*service.UserProfile, error) {
	f.gotID = id
	return f.profile, f.err
}

var (
	testUserID    = uuid.MustParse("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	testCompanyID = uuid.MustParse("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c")
)

func sampleUser() *model.User {
	email := "jane@acme.com"
	return &model.User{
		ID: testUserID, CompanyID: &testCompanyID, Email: &email,
		PasswordHash: "$2a$12$SECRET-HASH-MUST-NOT-LEAK",
		FirstName:    "Jane", LastName: "Smith", IsActive: true,
		CreatedAt: time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
	}
}

// userRouter registers the handler like the real router, with testCaller
// authenticated (or nobody, if caller is nil).
func userRouter(svc UserService, caller *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewUserHandler(svc)
	r := gin.New()
	if caller != nil {
		r.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), *caller))
		})
	}
	r.GET("/me", h.Me)
	g := r.Group("/api/v1/users")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.PATCH("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	g.PUT("/:id/password", h.SetPassword)
	return r
}

// ---------- Create ----------

func TestUserHandler_Create(t *testing.T) {
	svc := &fakeUserService{details: &service.UserDetails{User: sampleUser(), Roles: []string{"dispatcher"}}}

	rec := do(userRouter(svc, &testCaller), http.MethodPost, "/api/v1/users", `{
		"email": "jane@acme.com", "password": "correct horse",
		"first_name": "Jane", "last_name": "Smith", "roles": ["dispatcher"]
	}`)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/api/v1/users/"+testUserID.String(), rec.Header().Get("Location"))
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.Equal(t, "correct horse", svc.gotCreate.Password)
	assert.Nil(t, svc.gotCreate.CompanyID, "omitted company_id stays nil (the service decides)")
	assert.Contains(t, rec.Body.String(), `"roles":["dispatcher"]`)
	assert.NotContains(t, rec.Body.String(), "SECRET-HASH", "password hash must never be returned")
	assert.NotContains(t, rec.Body.String(), "password")
}

// ---------- List ----------

func TestUserHandler_List(t *testing.T) {
	t.Run("passes filters and returns page", func(t *testing.T) {
		svc := &fakeUserService{page: &service.UserPage{
			Items: []service.UserDetails{{User: sampleUser(), Roles: nil}}, Total: 1, Page: 1, PageSize: 20,
		}}

		rec := do(userRouter(svc, &testCaller), http.MethodGet,
			"/api/v1/users?company_id="+testCompanyID.String()+"&page=2&page_size=5", "")

		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, svc.gotList.CompanyID)
		assert.Equal(t, testCompanyID, *svc.gotList.CompanyID)
		assert.Equal(t, 2, svc.gotList.Page)
		assert.Equal(t, 5, svc.gotList.PageSize)
		assert.Contains(t, rec.Body.String(), `"roles":[]`, "nil roles become [] not null")
	})

	t.Run("invalid company_id", func(t *testing.T) {
		rec := do(userRouter(&fakeUserService{}, &testCaller), http.MethodGet, "/api/v1/users?company_id=nope", "")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, decodeError(t, rec).Details, "company_id")
	})
}

// ---------- Get / Update / Delete ----------

func TestUserHandler_Get(t *testing.T) {
	svc := &fakeUserService{details: &service.UserDetails{User: sampleUser(), Roles: []string{"driver"}}}

	rec := do(userRouter(svc, &testCaller), http.MethodGet, "/api/v1/users/"+testUserID.String(), "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testUserID, svc.gotID)
	assert.Equal(t, testCaller, svc.gotPrincipal)
}

func TestUserHandler_Update_PartialBody(t *testing.T) {
	svc := &fakeUserService{details: &service.UserDetails{User: sampleUser()}}

	t.Run("roles omitted stay nil (unchanged)", func(t *testing.T) {
		do(userRouter(svc, &testCaller), http.MethodPatch, "/api/v1/users/"+testUserID.String(), `{"is_active":false}`)

		require.NotNil(t, svc.gotUpdate.IsActive)
		assert.False(t, *svc.gotUpdate.IsActive)
		assert.Nil(t, svc.gotUpdate.Roles)
		assert.Nil(t, svc.gotUpdate.FirstName)
	})

	t.Run("roles sent replace all roles", func(t *testing.T) {
		do(userRouter(svc, &testCaller), http.MethodPatch, "/api/v1/users/"+testUserID.String(), `{"roles":["manager"]}`)

		require.NotNil(t, svc.gotUpdate.Roles)
		assert.Equal(t, []string{"manager"}, *svc.gotUpdate.Roles)
	})
}

func TestUserHandler_Delete(t *testing.T) {
	svc := &fakeUserService{}

	rec := do(userRouter(svc, &testCaller), http.MethodDelete, "/api/v1/users/"+testUserID.String(), "")

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, testUserID, svc.gotID)
}

func TestUserHandler_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		svcErr     error
		wantStatus int
	}{
		{"other tenant", http.MethodGet, "/api/v1/users/" + testUserID.String(), "", model.ErrNotFound, http.StatusNotFound},
		{"self-escalation", http.MethodPatch, "/api/v1/users/" + testUserID.String(), `{"roles":["admin"]}`, model.ErrForbidden, http.StatusForbidden},
		{"duplicate email", http.MethodPost, "/api/v1/users", `{}`, model.ErrEmailTaken, http.StatusConflict},
		{"validation", http.MethodPost, "/api/v1/users", `{}`, &validation.ValidationError{Fields: map[string]string{"roles": "x"}}, http.StatusBadRequest},
		{"bad id", http.MethodDelete, "/api/v1/users/not-a-uuid", "", nil, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(userRouter(&fakeUserService{err: tt.svcErr}, &testCaller), tt.method, tt.path, tt.body)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// ---------- Me ----------

func getMe(r http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	return rec
}

func TestUserHandler_Me(t *testing.T) {
	svc := &fakeUserService{profile: &service.UserProfile{
		User:        sampleUser(),
		Roles:       []string{"dispatcher"},
		Permissions: []model.Permission{model.PermDriverRead, model.PermTruckRead},
	}}
	caller := auth.Principal{UserID: testUserID, CompanyID: &testCompanyID}

	rec := getMe(userRouter(svc, &caller))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testUserID, svc.gotID, "profile is loaded for the token's user")
	assert.JSONEq(t, `{
		"id": "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		"company_id": "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c",
		"email": "jane@acme.com",
		"username": null,
		"first_name": "Jane",
		"last_name": "Smith",
		"is_active": true,
		"roles": ["dispatcher"],
		"last_login_at": null,
		"created_at": "2026-10-02T11:00:00Z",
		"permissions": ["driver:read", "truck:read"]
	}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "SECRET-HASH", "password hash must never be returned")
}

func TestUserHandler_Me_Errors(t *testing.T) {
	caller := &auth.Principal{UserID: uuid.New()}

	t.Run("no principal: route wired without Authenticate", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, getMe(userRouter(&fakeUserService{}, nil)).Code, "fail closed")
	})

	t.Run("user deleted after token was issued", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, getMe(userRouter(&fakeUserService{err: model.ErrNotFound}, caller)).Code)
	})

	t.Run("deactivated user", func(t *testing.T) {
		assert.Equal(t, http.StatusForbidden, getMe(userRouter(&fakeUserService{err: model.ErrUserInactive}, caller)).Code)
	})

	t.Run("unexpected error", func(t *testing.T) {
		assert.Equal(t, http.StatusInternalServerError, getMe(userRouter(&fakeUserService{err: errors.New("db down")}, caller)).Code)
	})
}

func TestUserHandler_SetPassword(t *testing.T) {
	svc := &fakeUserService{}

	rec := do(userRouter(svc, &testCaller), http.MethodPut, "/api/v1/users/"+testUserID.String()+"/password",
		`{"new_password":"temporary-pass-1"}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.Equal(t, testUserID, svc.gotID)
	assert.Equal(t, "temporary-pass-1", svc.gotPassword)
}

func TestUserHandler_SetPassword_Errors(t *testing.T) {
	tests := []struct {
		name       string
		path, body string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{"own account", testUserID.String(), `{"new_password":"temporary-pass-1"}`, model.ErrForbidden, http.StatusForbidden, apierror.CodeForbidden},
		{"disabled user", testUserID.String(), `{"new_password":"temporary-pass-1"}`, model.ErrUserInactive, http.StatusForbidden, apierror.CodeForbidden},
		{"not in your company", testUserID.String(), `{"new_password":"temporary-pass-1"}`, model.ErrNotFound, http.StatusNotFound, apierror.CodeNotFound},
		{"weak password", testUserID.String(), `{"new_password":"short"}`,
			&validation.ValidationError{Fields: map[string]string{"new_password": "must be at least 8 characters"}}, http.StatusBadRequest, apierror.CodeValidation},
		{"bad id", "not-a-uuid", `{"new_password":"temporary-pass-1"}`, nil, http.StatusBadRequest, apierror.CodeValidation},
		{"wrong field", testUserID.String(), `{"password":"temporary-pass-1"}`, nil, http.StatusBadRequest, apierror.CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeUserService{err: tc.svcErr}

			rec := do(userRouter(svc, &testCaller), http.MethodPut, "/api/v1/users/"+tc.path+"/password", tc.body)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantCode, decodeError(t, rec).Code)
		})
	}
}
