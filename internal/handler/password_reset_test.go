package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

type fakePasswordResetService struct {
	gotEmail, gotCode, gotToken, gotPassword string
	requestErr, verifyErr, resetErr          error
}

func (f *fakePasswordResetService) RequestReset(_ context.Context, email string) error {
	f.gotEmail = email
	return f.requestErr
}

func (f *fakePasswordResetService) VerifyCode(_ context.Context, email, code string) (string, time.Duration, error) {
	f.gotEmail, f.gotCode = email, code
	if f.verifyErr != nil {
		return "", 0, f.verifyErr
	}
	return "reset-token", 10 * time.Minute, nil
}

func (f *fakePasswordResetService) ResetPassword(_ context.Context, token, pw string) error {
	f.gotToken, f.gotPassword = token, pw
	return f.resetErr
}

func newPasswordRouter(svc PasswordResetService, lim ResetRequestLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewPasswordResetHandler(svc, lim)
	r := gin.New()
	g := r.Group("/api/v1/auth/password")
	g.POST("/forgot", h.Forgot)
	g.POST("/verify", h.Verify)
	g.POST("/reset", h.Reset)
	return r
}

func TestPasswordReset_Forgot(t *testing.T) {
	svc := &fakePasswordResetService{}
	lim := &fakeLimiter{}

	rec := do(newPasswordRouter(svc, lim), http.MethodPost, "/api/v1/auth/password/forgot", `{"email":" Jane@Acme.com "}`)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.JSONEq(t, `{"message":"If an account with this email exists, a reset code has been sent."}`, rec.Body.String())
	assert.Equal(t, " Jane@Acme.com ", svc.gotEmail)
	assert.Equal(t, []string{"jane@acme.com"}, lim.taken, "limited per normalized address")
}

func TestPasswordReset_ForgotRateLimited(t *testing.T) {
	svc := &fakePasswordResetService{}

	rec := do(newPasswordRouter(svc, &fakeLimiter{blocked: true}), http.MethodPost,
		"/api/v1/auth/password/forgot", `{"email":"jane@acme.com"}`)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Empty(t, svc.gotEmail, "no email is sent")
}

func TestPasswordReset_ForgotInvalidEmail(t *testing.T) {
	svc := &fakePasswordResetService{requestErr: &validation.ValidationError{Fields: map[string]string{"email": "must be a valid email address"}}}

	rec := do(newPasswordRouter(svc, &fakeLimiter{}), http.MethodPost, "/api/v1/auth/password/forgot", `{"email":"nope"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, apierror.CodeValidation, decodeError(t, rec).Code)
}

func TestPasswordReset_Verify(t *testing.T) {
	svc := &fakePasswordResetService{}

	rec := do(newPasswordRouter(svc, &fakeLimiter{}), http.MethodPost, "/api/v1/auth/password/verify",
		`{"email":"jane@acme.com","code":"048213"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "048213", svc.gotCode, "a string: the leading zero survives")
	var got VerifyResetCodeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, VerifyResetCodeResponse{ResetToken: "reset-token", ExpiresIn: 600}, got)
}

func TestPasswordReset_Reset(t *testing.T) {
	svc := &fakePasswordResetService{}

	rec := do(newPasswordRouter(svc, &fakeLimiter{}), http.MethodPost, "/api/v1/auth/password/reset",
		`{"reset_token":"tok","new_password":"new-strong-password"}`)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "tok", svc.gotToken)
	assert.Equal(t, "new-strong-password", svc.gotPassword)
}

func TestPasswordReset_Errors(t *testing.T) {
	tests := []struct {
		name       string
		path, body string
		svc        *fakePasswordResetService
		wantStatus int
		wantCode   string
	}{
		{"wrong code", "/verify", `{"email":"a@b.co","code":"000000"}`,
			&fakePasswordResetService{verifyErr: model.ErrResetCodeInvalid}, http.StatusBadRequest, apierror.CodeInvalidResetCode},
		{"bad token", "/reset", `{"reset_token":"x","new_password":"long-enough"}`,
			&fakePasswordResetService{resetErr: model.ErrResetTokenInvalid}, http.StatusBadRequest, apierror.CodeInvalidResetToken},
		{"weak password", "/reset", `{"reset_token":"x","new_password":"short"}`,
			&fakePasswordResetService{resetErr: &validation.ValidationError{Fields: map[string]string{"new_password": "too short"}}},
			http.StatusBadRequest, apierror.CodeValidation},
		{"code as number", "/verify", `{"email":"a@b.co","code":48213}`,
			&fakePasswordResetService{}, http.StatusBadRequest, apierror.CodeValidation},
		{"unknown field", "/forgot", `{"mail":"a@b.co"}`,
			&fakePasswordResetService{}, http.StatusBadRequest, apierror.CodeValidation},
		{"unexpected", "/forgot", `{"email":"a@b.co"}`,
			&fakePasswordResetService{requestErr: errors.New("db down")}, http.StatusInternalServerError, apierror.CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newPasswordRouter(tc.svc, &fakeLimiter{}), http.MethodPost, "/api/v1/auth/password"+tc.path, tc.body)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantCode, decodeError(t, rec).Code)
		})
	}
}
