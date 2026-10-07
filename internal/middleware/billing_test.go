package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeBilling struct {
	status string
	err    error
	calls  int
}

func (f *fakeBilling) BillingStatus(context.Context, uuid.UUID) (string, error) {
	f.calls++
	return f.status, f.err
}

func billingRouter(loader BillingStatusLoader, p *auth.Principal) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if p != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), *p))
		}
	})
	r.GET("/trucks", RequireActiveBilling(loader), func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestRequireActiveBilling(t *testing.T) {
	company := uuid.New()
	user := auth.Principal{UserID: uuid.New(), CompanyID: &company}

	cases := map[string]int{
		model.BillingActive:  http.StatusOK,
		model.BillingPastDue: http.StatusOK, // grace period
		model.BillingNone:    http.StatusPaymentRequired,
		model.BillingPending: http.StatusPaymentRequired,
		model.BillingBlocked: http.StatusPaymentRequired,
	}
	for status, want := range cases {
		t.Run(status, func(t *testing.T) {
			rec := httptest.NewRecorder()
			billingRouter(&fakeBilling{status: status}, &user).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trucks", nil))

			assert.Equal(t, want, rec.Code)
			assert.Equal(t, status, rec.Header().Get(BillingStatusHeader), "the page can warn during the grace period")
			if want == http.StatusPaymentRequired {
				assert.Contains(t, rec.Body.String(), `"code":"billing_inactive"`)
				assert.Contains(t, rec.Body.String(), `"billing_status":"`+status+`"`)
			}
		})
	}
}

func TestRequireActiveBilling_PlatformUsersAreNotBilled(t *testing.T) {
	loader := &fakeBilling{status: model.BillingBlocked}
	admin := auth.Principal{UserID: uuid.New()}
	rec := httptest.NewRecorder()

	billingRouter(loader, &admin).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trucks", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Zero(t, loader.calls)
}

func TestRequireActiveBilling_FailsClosed(t *testing.T) {
	company := uuid.New()
	user := auth.Principal{UserID: uuid.New(), CompanyID: &company}

	rec := httptest.NewRecorder()
	billingRouter(&fakeBilling{err: errors.New("db down")}, &user).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trucks", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "an outage never means free access")

	rec = httptest.NewRecorder()
	billingRouter(&fakeBilling{status: model.BillingActive}, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/trucks", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "no principal: wiring bug, refuse")
}
