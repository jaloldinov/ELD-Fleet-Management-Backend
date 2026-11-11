package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// fakeCompanyService is a hand-written stub (same idea as the fake repository
// in the service tests): unset functions fail the test if called.
type fakeCompanyService struct {
	t            *testing.T
	gotPrincipal auth.Principal
	createFn     func(in service.CreateCompanyInput) (*model.Company, error)
	getFn        func(id uuid.UUID) (*model.Company, error)
	listFn       func(p service.ListParams) (*service.CompanyPage, error)
	updateFn     func(id uuid.UUID, in service.UpdateCompanyInput) (*model.Company, error)
	deleteFn     func(id uuid.UUID) error
}

func (f *fakeCompanyService) Create(_ context.Context, principal auth.Principal, in service.CreateCompanyInput) (*model.Company, error) {
	f.gotPrincipal = principal
	if f.createFn == nil {
		f.t.Fatal("unexpected call to Create")
	}
	return f.createFn(in)
}

func (f *fakeCompanyService) GetByID(_ context.Context, principal auth.Principal, id uuid.UUID) (*model.Company, error) {
	f.gotPrincipal = principal
	if f.getFn == nil {
		f.t.Fatal("unexpected call to GetByID")
	}
	return f.getFn(id)
}

func (f *fakeCompanyService) List(_ context.Context, principal auth.Principal, p service.ListParams) (*service.CompanyPage, error) {
	f.gotPrincipal = principal
	if f.listFn == nil {
		f.t.Fatal("unexpected call to List")
	}
	return f.listFn(p)
}

func (f *fakeCompanyService) Update(_ context.Context, principal auth.Principal, id uuid.UUID, in service.UpdateCompanyInput) (*model.Company, error) {
	f.gotPrincipal = principal
	if f.updateFn == nil {
		f.t.Fatal("unexpected call to Update")
	}
	return f.updateFn(id, in)
}

func (f *fakeCompanyService) Delete(_ context.Context, principal auth.Principal, id uuid.UUID) error {
	f.gotPrincipal = principal
	if f.deleteFn == nil {
		f.t.Fatal("unexpected call to Delete")
	}
	return f.deleteFn(id)
}

// testCaller is the authenticated principal in handler tests.
var testCaller = auth.Principal{UserID: uuid.MustParse("0199a1b2-0000-7000-8000-000000000001")}

// newCompanyRouter registers the handler on the same paths as the real router,
// with testCaller authenticated (what the Authenticate middleware would do).
func newCompanyRouter(svc CompanyService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewCompanyHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	g := r.Group("/api/v1/companies")
	g.POST("", h.Create)
	g.GET("", h.List)
	g.GET("/:id", h.Get)
	g.PATCH("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	return r
}

func do(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) apierror.Body {
	t.Helper()
	var resp apierror.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body: %s", rec.Body.String())
	return resp.Error
}

func sampleCompany() *model.Company {
	customer := "cus_internal"
	// A non-UTC time proves the handler converts to UTC.
	tashkent := time.FixedZone("UTC+5", 5*60*60)
	return &model.Company{
		ID:        uuid.MustParse("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"),
		Name:      "Acme",
		DOTNumber: "1234567",
		Timezone:  "America/Chicago",
		HOSCycle:  model.HOSCycle70in8,
		IsActive:  true,
		// Billing data: billing_status is shown, the Stripe customer id is not.
		StripeCustomerID: &customer,
		BillingStatus:    model.BillingActive,
		CreatedAt:        time.Date(2026, 10, 2, 16, 0, 0, 0, tashkent),
		UpdatedAt:        time.Date(2026, 10, 2, 16, 0, 0, 0, tashkent),
		DeletedAt:        nil,
	}
}

// ---------- Create ----------

func TestCompanyHandler_Create_Success(t *testing.T) {
	var gotInput service.CreateCompanyInput
	svc := &fakeCompanyService{t: t, createFn: func(in service.CreateCompanyInput) (*model.Company, error) {
		gotInput = in
		return sampleCompany(), nil
	}}

	rec := do(newCompanyRouter(svc), http.MethodPost, "/api/v1/companies",
		`{"name":"Acme","dot_number":"1234567","timezone":"America/Chicago","hos_cycle":"us_60_7"}`)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/api/v1/companies/0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", rec.Header().Get("Location"))
	assert.Equal(t, service.CreateCompanyInput{Name: "Acme", DOTNumber: "1234567", Timezone: "America/Chicago", HOSCycle: "us_60_7"}, gotInput)
	assert.JSONEq(t, `{
		"id": "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		"name": "Acme",
		"dot_number": "1234567",
		"timezone": "America/Chicago",
		"hos_cycle": "us_70_8",
		"is_active": true,
		"billing_status": "active",
		"created_at": "2026-10-02T11:00:00Z",
		"updated_at": "2026-10-02T11:00:00Z"
	}`, rec.Body.String(), "times are UTC; deleted_at and the Stripe customer id are never exposed")
}

func TestCompanyHandler_Create_InvalidJSON(t *testing.T) {
	svc := &fakeCompanyService{t: t} // service must not be called

	rec := do(newCompanyRouter(svc), http.MethodPost, "/api/v1/companies", `{"name": `)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, apierror.CodeBadRequest, decodeError(t, rec).Code)
}

func TestCompanyHandler_Create_ErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		svcErr     error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "validation error",
			svcErr:     &validation.ValidationError{Fields: map[string]string{"dot_number": "must be 1 to 8 digits"}},
			wantStatus: http.StatusBadRequest,
			wantCode:   apierror.CodeValidation,
		},
		{name: "duplicate DOT", svcErr: model.ErrDOTNumberTaken, wantStatus: http.StatusConflict, wantCode: apierror.CodeConflict},
		{name: "unexpected error", svcErr: errors.New("db exploded: secret details"), wantStatus: http.StatusInternalServerError, wantCode: apierror.CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeCompanyService{t: t, createFn: func(service.CreateCompanyInput) (*model.Company, error) {
				return nil, tt.svcErr
			}}

			rec := do(newCompanyRouter(svc), http.MethodPost, "/api/v1/companies", `{}`)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantCode, decodeError(t, rec).Code)
			assert.NotContains(t, rec.Body.String(), "secret details", "internal errors must not leak")
		})
	}
}

func TestCompanyHandler_Create_ValidationDetails(t *testing.T) {
	svc := &fakeCompanyService{t: t, createFn: func(service.CreateCompanyInput) (*model.Company, error) {
		return nil, &validation.ValidationError{Fields: map[string]string{"dot_number": "must be 1 to 8 digits"}}
	}}

	rec := do(newCompanyRouter(svc), http.MethodPost, "/api/v1/companies", `{}`)

	assert.Equal(t, map[string]string{"dot_number": "must be 1 to 8 digits"}, decodeError(t, rec).Details)
}

// ---------- Get ----------

func TestCompanyHandler_Get(t *testing.T) {
	company := sampleCompany()

	t.Run("found", func(t *testing.T) {
		svc := &fakeCompanyService{t: t, getFn: func(id uuid.UUID) (*model.Company, error) {
			assert.Equal(t, company.ID, id)
			return company, nil
		}}

		rec := do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies/"+company.ID.String(), "")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"name":"Acme"`)
	})

	t.Run("not found", func(t *testing.T) {
		svc := &fakeCompanyService{t: t, getFn: func(uuid.UUID) (*model.Company, error) {
			return nil, model.ErrNotFound
		}}

		rec := do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies/"+uuid.NewString(), "")

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, apierror.CodeNotFound, decodeError(t, rec).Code)
	})

	t.Run("invalid uuid", func(t *testing.T) {
		svc := &fakeCompanyService{t: t} // must not be called

		rec := do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies/not-a-uuid", "")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, map[string]string{"id": "must be a valid UUID"}, decodeError(t, rec).Details)
	})
}

// ---------- List ----------

func TestCompanyHandler_List(t *testing.T) {
	t.Run("passes query params and returns page", func(t *testing.T) {
		var got service.ListParams
		svc := &fakeCompanyService{t: t, listFn: func(p service.ListParams) (*service.CompanyPage, error) {
			got = p
			return &service.CompanyPage{Items: []model.Company{*sampleCompany()}, Total: 21, Page: 2, PageSize: 20}, nil
		}}

		rec := do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies?page=2&page_size=20", "")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, service.ListParams{Page: 2, PageSize: 20}, got)
		var body ListCompaniesResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Len(t, body.Items, 1)
		assert.Equal(t, 21, body.Total)
		assert.Equal(t, 2, body.Page)
	})

	t.Run("empty page is [] not null", func(t *testing.T) {
		svc := &fakeCompanyService{t: t, listFn: func(service.ListParams) (*service.CompanyPage, error) {
			return &service.CompanyPage{Items: nil, Total: 0, Page: 1, PageSize: 20}, nil
		}}

		rec := do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies", "")

		assert.Contains(t, rec.Body.String(), `"items":[]`)
	})

	t.Run("non-integer params", func(t *testing.T) {
		svc := &fakeCompanyService{t: t} // must not be called

		rec := do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies?page=abc&page_size=x", "")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		details := decodeError(t, rec).Details
		assert.Contains(t, details, "page")
		assert.Contains(t, details, "page_size")
	})
}

// ---------- Update ----------

func TestCompanyHandler_Update_PartialBody(t *testing.T) {
	company := sampleCompany()
	var got service.UpdateCompanyInput
	svc := &fakeCompanyService{t: t, updateFn: func(id uuid.UUID, in service.UpdateCompanyInput) (*model.Company, error) {
		got = in
		return company, nil
	}}

	rec := do(newCompanyRouter(svc), http.MethodPatch, "/api/v1/companies/"+company.ID.String(),
		`{"is_active": false}`)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got.IsActive, "sent false must arrive as a non-nil pointer")
	assert.False(t, *got.IsActive)
	assert.Nil(t, got.Name, "fields not sent stay nil")
	assert.Nil(t, got.DOTNumber)
	assert.Nil(t, got.Timezone)
}

// ---------- Delete ----------

func TestCompanyHandler_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		svc := &fakeCompanyService{t: t, deleteFn: func(uuid.UUID) error { return nil }}

		rec := do(newCompanyRouter(svc), http.MethodDelete, "/api/v1/companies/"+uuid.NewString(), "")

		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Empty(t, rec.Body.String())
	})

	t.Run("not found", func(t *testing.T) {
		svc := &fakeCompanyService{t: t, deleteFn: func(uuid.UUID) error { return model.ErrNotFound }}

		rec := do(newCompanyRouter(svc), http.MethodDelete, "/api/v1/companies/"+uuid.NewString(), "")

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

// ---------- Principal and tenant errors ----------

func TestCompanyHandler_PassesAuthenticatedPrincipal(t *testing.T) {
	svc := &fakeCompanyService{t: t, getFn: func(uuid.UUID) (*model.Company, error) { return sampleCompany(), nil }}

	do(newCompanyRouter(svc), http.MethodGet, "/api/v1/companies/"+uuid.NewString(), "")

	assert.Equal(t, testCaller, svc.gotPrincipal, "the service must receive the caller from the token")
}

func TestCompanyHandler_ForbiddenFromService(t *testing.T) {
	svc := &fakeCompanyService{t: t, updateFn: func(uuid.UUID, service.UpdateCompanyInput) (*model.Company, error) {
		return nil, model.ErrForbidden
	}}

	rec := do(newCompanyRouter(svc), http.MethodPatch, "/api/v1/companies/"+uuid.NewString(), `{"is_active":false}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, apierror.CodeForbidden, decodeError(t, rec).Code)
}
