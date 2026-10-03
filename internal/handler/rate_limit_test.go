package handler

import (
	"context"
	"encoding/json"
	"net/http"
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

type fakeRateLimitService struct {
	t            *testing.T
	gotPrincipal auth.Principal
	gotName      string
	gotInput     service.UpdateRateLimitInput
	listFn       func() ([]model.RateLimit, error)
	getFn        func(name string) (*model.RateLimit, error)
	updateFn     func(name string, in service.UpdateRateLimitInput) (*model.RateLimit, error)
}

func (f *fakeRateLimitService) List(_ context.Context, p auth.Principal) ([]model.RateLimit, error) {
	f.gotPrincipal = p
	if f.listFn == nil {
		f.t.Fatal("unexpected call to List")
	}
	return f.listFn()
}

func (f *fakeRateLimitService) Get(_ context.Context, p auth.Principal, name string) (*model.RateLimit, error) {
	f.gotPrincipal, f.gotName = p, name
	if f.getFn == nil {
		f.t.Fatal("unexpected call to Get")
	}
	return f.getFn(name)
}

func (f *fakeRateLimitService) Update(_ context.Context, p auth.Principal, name string, in service.UpdateRateLimitInput) (*model.RateLimit, error) {
	f.gotPrincipal, f.gotName, f.gotInput = p, name, in
	if f.updateFn == nil {
		f.t.Fatal("unexpected call to Update")
	}
	return f.updateFn(name, in)
}

func newRateLimitRouter(svc RateLimitService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewRateLimitHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/rate-limits")
	g.GET("", h.List)
	g.GET("/:name", h.Get)
	g.PATCH("/:name", h.Update)
	return r
}

func sampleRateLimit() *model.RateLimit {
	by := uuid.MustParse("0199a1b2-0000-7000-8000-000000000009")
	return &model.RateLimit{
		Name: model.RateLimitLoginAttempts, Limit: 5, Window: 15 * time.Minute,
		Description: "Login attempts", UpdatedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC), UpdatedBy: &by,
	}
}

func TestRateLimitHandler_List(t *testing.T) {
	svc := &fakeRateLimitService{t: t, listFn: func() ([]model.RateLimit, error) {
		return []model.RateLimit{*sampleRateLimit()}, nil
	}}

	rec := do(newRateLimitRouter(svc), http.MethodGet, "/api/v1/rate-limits", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, testCaller, svc.gotPrincipal)
	assert.JSONEq(t, `{"items":[{
		"name":"login_attempts","description":"Login attempts",
		"limit":5,"window_seconds":900,
		"updated_at":"2026-10-03T09:00:00Z","updated_by":"0199a1b2-0000-7000-8000-000000000009"
	}]}`, rec.Body.String())
}

func TestRateLimitHandler_Get(t *testing.T) {
	svc := &fakeRateLimitService{t: t, getFn: func(string) (*model.RateLimit, error) { return sampleRateLimit(), nil }}

	rec := do(newRateLimitRouter(svc), http.MethodGet, "/api/v1/rate-limits/login_attempts", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "login_attempts", svc.gotName)
	var got RateLimitResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, 900, got.WindowSeconds)
}

func TestRateLimitHandler_Update(t *testing.T) {
	svc := &fakeRateLimitService{t: t, updateFn: func(string, service.UpdateRateLimitInput) (*model.RateLimit, error) {
		rl := sampleRateLimit()
		rl.Limit, rl.Window = 3, 10*time.Minute
		return rl, nil
	}}

	rec := do(newRateLimitRouter(svc), http.MethodPatch, "/api/v1/rate-limits/login_attempts",
		`{"limit":3,"window_seconds":600}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "login_attempts", svc.gotName)
	require.NotNil(t, svc.gotInput.Limit)
	require.NotNil(t, svc.gotInput.WindowSeconds)
	assert.Equal(t, 3, *svc.gotInput.Limit)
	assert.Equal(t, 600, *svc.gotInput.WindowSeconds)
	var got RateLimitResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, 3, got.Limit)
	assert.Equal(t, 600, got.WindowSeconds)
}

func TestRateLimitHandler_UpdatePartialLeavesOtherFieldNil(t *testing.T) {
	svc := &fakeRateLimitService{t: t, updateFn: func(string, service.UpdateRateLimitInput) (*model.RateLimit, error) {
		return sampleRateLimit(), nil
	}}

	rec := do(newRateLimitRouter(svc), http.MethodPatch, "/api/v1/rate-limits/auth_requests", `{"limit":50}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, svc.gotInput.WindowSeconds, "omitted = unchanged")
}

func TestRateLimitHandler_Errors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path, body string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{"unknown rule", http.MethodGet, "/api/v1/rate-limits/nope", "", model.ErrNotFound, http.StatusNotFound, apierror.CodeNotFound},
		{"company user", http.MethodGet, "/api/v1/rate-limits/login_attempts", "", model.ErrForbidden, http.StatusForbidden, apierror.CodeForbidden},
		{"validation", http.MethodPatch, "/api/v1/rate-limits/login_attempts", `{"limit":0}`,
			&validation.ValidationError{Fields: map[string]string{"limit": "must be between 1 and 10000"}},
			http.StatusBadRequest, apierror.CodeValidation},
		{"invalid JSON", http.MethodPatch, "/api/v1/rate-limits/login_attempts", `{"limit":`, nil, http.StatusBadRequest, apierror.CodeBadRequest},
		{"wrong type", http.MethodPatch, "/api/v1/rate-limits/login_attempts", `{"limit":"5"}`, nil, http.StatusBadRequest, apierror.CodeValidation},
		{"unknown field", http.MethodPatch, "/api/v1/rate-limits/login_attempts", `{"window_minutes":15}`, nil, http.StatusBadRequest, apierror.CodeValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fail := func() error { return tc.svcErr }
			svc := &fakeRateLimitService{t: t,
				getFn: func(string) (*model.RateLimit, error) { return nil, fail() },
				updateFn: func(string, service.UpdateRateLimitInput) (*model.RateLimit, error) {
					if tc.svcErr == nil {
						t.Fatal("the service must not be called for a bad body")
					}
					return nil, fail()
				},
			}

			rec := do(newRateLimitRouter(svc), tc.method, tc.path, tc.body)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantCode, decodeError(t, rec).Code)
		})
	}
}
