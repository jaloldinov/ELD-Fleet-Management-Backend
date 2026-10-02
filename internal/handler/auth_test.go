package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeAuthService struct {
	gotLogin, gotPassword string
	res                   *service.LoginResult
	err                   error
}

func (f *fakeAuthService) Login(_ context.Context, login, password string) (*service.LoginResult, error) {
	f.gotLogin, f.gotPassword = login, password
	return f.res, f.err
}

func newAuthRouter(svc AuthService, now time.Time) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(svc)
	h.now = func() time.Time { return now }
	r := gin.New()
	r.POST("/api/v1/auth/login", h.Login)
	return r
}

func TestAuthHandler_Login_Success(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc := &fakeAuthService{res: &service.LoginResult{
		AccessToken: "signed-token",
		ExpiresAt:   now.Add(15 * time.Minute),
	}}

	rec := do(newAuthRouter(svc, now), http.MethodPost, "/api/v1/auth/login",
		`{"login":"jane@acme.com","password":"correct horse"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"access_token":"signed-token","token_type":"Bearer","expires_in":900}`, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "jane@acme.com", svc.gotLogin)
	assert.Equal(t, "correct horse", svc.gotPassword)
}

func TestAuthHandler_Login_Errors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{"invalid JSON", `{"login":`, nil, http.StatusBadRequest, CodeBadRequest},
		{"bad credentials", `{}`, model.ErrInvalidCredentials, http.StatusUnauthorized, CodeUnauthorized},
		{"disabled account", `{}`, model.ErrUserInactive, http.StatusForbidden, CodeForbidden},
		{"unexpected", `{}`, errors.New("db down"), http.StatusInternalServerError, CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAuthService{err: tt.svcErr}

			rec := do(newAuthRouter(svc, time.Now()), http.MethodPost, "/api/v1/auth/login", tt.body)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantCode, decodeError(t, rec).Code)
		})
	}
}
