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

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeUserService struct {
	gotID   uuid.UUID
	profile *service.UserProfile
	err     error
}

func (f *fakeUserService) GetProfile(_ context.Context, id uuid.UUID) (*service.UserProfile, error) {
	f.gotID = id
	return f.profile, f.err
}

// meRouter simulates the Authenticate middleware by putting principal (if not
// nil) into the request context.
func meRouter(svc UserService, principal *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if principal != nil {
		r.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), *principal))
		})
	}
	r.GET("/me", NewUserHandler(svc).Me)
	return r
}

func getMe(r http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	return rec
}

func TestUserHandler_Me(t *testing.T) {
	companyID := uuid.MustParse("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c")
	userID := uuid.MustParse("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	email := "jane@acme.com"
	svc := &fakeUserService{profile: &service.UserProfile{
		User: &model.User{
			ID: userID, CompanyID: &companyID, Email: &email,
			PasswordHash: "$2a$12$SECRET-HASH-MUST-NOT-LEAK",
			FirstName:    "Jane", LastName: "Smith", IsActive: true,
			CreatedAt: time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
		},
		Permissions: []model.Permission{model.PermDriverRead, model.PermTruckRead},
	}}

	rec := getMe(meRouter(svc, &auth.Principal{UserID: userID, CompanyID: &companyID}))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, userID, svc.gotID, "profile is loaded for the token's user")
	assert.JSONEq(t, `{
		"id": "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		"company_id": "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c",
		"email": "jane@acme.com",
		"username": null,
		"first_name": "Jane",
		"last_name": "Smith",
		"is_active": true,
		"last_login_at": null,
		"created_at": "2026-10-02T11:00:00Z",
		"permissions": ["driver:read", "truck:read"]
	}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "SECRET-HASH", "password hash must never be returned")
	assert.NotContains(t, rec.Body.String(), "password")
}

func TestUserHandler_Me_Errors(t *testing.T) {
	p := &auth.Principal{UserID: uuid.New()}

	t.Run("no principal: route wired without Authenticate", func(t *testing.T) {
		rec := getMe(meRouter(&fakeUserService{}, nil))

		assert.Equal(t, http.StatusUnauthorized, rec.Code, "fail closed")
	})

	t.Run("user deleted after token was issued", func(t *testing.T) {
		rec := getMe(meRouter(&fakeUserService{err: model.ErrNotFound}, p))

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("unexpected error", func(t *testing.T) {
		rec := getMe(meRouter(&fakeUserService{err: errors.New("db down")}, p))

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
