package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeAuthService struct {
	gotLogin, gotPassword, gotRefresh string
	gotClient                         model.ClientInfo
	pair                              *service.TokenPair
	err                               error
}

func (f *fakeAuthService) Login(_ context.Context, login, password string, client model.ClientInfo) (*service.TokenPair, error) {
	f.gotLogin, f.gotPassword, f.gotClient = login, password, client
	return f.pair, f.err
}

func (f *fakeAuthService) Refresh(_ context.Context, token string, client model.ClientInfo) (*service.TokenPair, error) {
	f.gotRefresh, f.gotClient = token, client
	return f.pair, f.err
}

func (f *fakeAuthService) Logout(_ context.Context, token string) error {
	f.gotRefresh = token
	return f.err
}

var authTestNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func newAuthRouter(svc AuthService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(svc)
	h.now = func() time.Time { return authTestNow }
	r := gin.New()
	r.POST("/api/v1/auth/login", h.Login)
	r.POST("/api/v1/auth/refresh", h.Refresh)
	r.POST("/api/v1/auth/logout", h.Logout)
	return r
}

func samplePair() *service.TokenPair {
	return &service.TokenPair{
		AccessToken: "access", AccessExpiresAt: authTestNow.Add(15 * time.Minute),
		RefreshToken: "refresh", RefreshExpiresAt: authTestNow.Add(30 * 24 * time.Hour),
	}
}

const wantTokenJSON = `{
	"access_token": "access", "token_type": "Bearer", "expires_in": 900,
	"refresh_token": "refresh", "refresh_expires_in": 2592000
}`

func TestAuthHandler_Login_Success(t *testing.T) {
	svc := &fakeAuthService{pair: samplePair()}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"login":"jane@acme.com","password":"correct horse"}`))
	req.Header.Set("User-Agent", "ELD-App/1.0")
	rec := httptest.NewRecorder()

	newAuthRouter(svc).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, wantTokenJSON, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "jane@acme.com", svc.gotLogin)
	assert.Equal(t, "correct horse", svc.gotPassword)
	assert.Equal(t, "ELD-App/1.0", svc.gotClient.UserAgent, "client info is passed to the session")
	assert.NotEmpty(t, svc.gotClient.IP)
}

func TestAuthHandler_Refresh_Success(t *testing.T) {
	svc := &fakeAuthService{pair: samplePair()}

	rec := do(newAuthRouter(svc), http.MethodPost, "/api/v1/auth/refresh", `{"refresh_token":"old"}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, wantTokenJSON, rec.Body.String())
	assert.Equal(t, "old", svc.gotRefresh)
}

func TestAuthHandler_Logout(t *testing.T) {
	svc := &fakeAuthService{}

	rec := do(newAuthRouter(svc), http.MethodPost, "/api/v1/auth/logout", `{"refresh_token":"mine"}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "mine", svc.gotRefresh)
}

func TestAuthHandler_Errors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{"login: invalid JSON", "/api/v1/auth/login", `{"login":`, nil, http.StatusBadRequest, CodeBadRequest},
		{"login: bad credentials", "/api/v1/auth/login", `{}`, model.ErrInvalidCredentials, http.StatusUnauthorized, CodeUnauthorized},
		{"login: disabled account", "/api/v1/auth/login", `{}`, model.ErrUserInactive, http.StatusForbidden, CodeForbidden},
		{"login: unexpected", "/api/v1/auth/login", `{}`, errors.New("db down"), http.StatusInternalServerError, CodeInternal},
		{"refresh: invalid token", "/api/v1/auth/refresh", `{}`, model.ErrRefreshTokenInvalid, http.StatusUnauthorized, CodeUnauthorized},
		{"refresh: reused token", "/api/v1/auth/refresh", `{}`, model.ErrRefreshTokenReused, http.StatusUnauthorized, CodeUnauthorized},
		{"logout: invalid JSON", "/api/v1/auth/logout", `nope`, nil, http.StatusBadRequest, CodeBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeAuthService{err: tt.svcErr}

			rec := do(newAuthRouter(svc), http.MethodPost, tt.path, tt.body)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantCode, decodeError(t, rec).Code)
		})
	}
}

// Reuse must look EXACTLY like an invalid token to the client (an attacker
// learns nothing), while being recorded for the logs via c.Errors.
func TestAuthHandler_Refresh_ReuseLooksLikeInvalidButIsRecorded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var recorded []*gin.Error
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Next(); recorded = c.Errors })
	h := NewAuthHandler(&fakeAuthService{err: model.ErrRefreshTokenReused})
	r.POST("/refresh", h.Refresh)

	reused := do(r, http.MethodPost, "/refresh", `{}`)
	invalid := do(newAuthRouter(&fakeAuthService{err: model.ErrRefreshTokenInvalid}),
		http.MethodPost, "/api/v1/auth/refresh", `{}`)

	assert.Equal(t, invalid.Body.String(), reused.Body.String(), "identical response bodies")
	require.Len(t, recorded, 1)
	assert.ErrorIs(t, recorded[0].Err, model.ErrRefreshTokenReused)
}

func TestClientInfo_TruncatesUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &fakeAuthService{pair: samplePair()}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
	req.Header.Set("User-Agent", strings.Repeat("A", 10_000))

	newAuthRouter(svc).ServeHTTP(httptest.NewRecorder(), req)

	assert.Len(t, svc.gotClient.UserAgent, maxUserAgentLen)
}
