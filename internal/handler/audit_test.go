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

	"github.com/jaloldinov/ELD-learning/internal/apierror"
	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

type fakeAuditLogService struct {
	gotPrincipal auth.Principal
	gotParams    service.AuditLogListParams
	page         *service.AuditLogPage
}

func (f *fakeAuditLogService) List(_ context.Context, p auth.Principal, params service.AuditLogListParams) (*service.AuditLogPage, error) {
	f.gotPrincipal, f.gotParams = p, params
	return f.page, nil
}

func newAuditRouter(svc AuditLogService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAuditLogHandler(svc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), testCaller))
	})
	r.GET("/api/v1/audit-logs", h.List)
	return r
}

func TestAuditLogHandler_List(t *testing.T) {
	actor := uuid.MustParse("0199a1b2-0000-7000-8000-00000000000a")
	company := uuid.MustParse("0199a1b2-0000-7000-8000-00000000000c")
	entryID := uuid.MustParse("0199a1b2-0000-7000-8000-0000000000e1")
	svc := &fakeAuditLogService{page: &service.AuditLogPage{Page: 1, PageSize: 20, Total: 2, Items: []model.AuditEntry{
		{
			ID: entryID, OccurredAt: time.Date(2026, 3, 10, 9, 12, 0, 0, time.UTC),
			ActorID: &actor, ActorName: "Jane Smith", CompanyID: &company,
			Action: "truck.update", Entity: "truck", EntityID: "t1",
			Changes:   model.AuditChanges{"license_plate": {Old: "ABC", New: "XYZ"}},
			RequestID: "req-1",
		},
		{ID: entryID, OccurredAt: time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC), Action: "user.create", Entity: "user", EntityID: "u1"},
	}}}

	rec := do(newAuditRouter(svc), http.MethodGet,
		"/api/v1/audit-logs?entity=truck&entity_id=t1&from=2024-01-01T00:00:00Z&page=1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "truck", svc.gotParams.Entity)
	assert.Equal(t, "t1", svc.gotParams.EntityID)
	require.NotNil(t, svc.gotParams.From)
	assert.Equal(t, 2024, svc.gotParams.From.Year())
	assert.JSONEq(t, `{"page":1,"page_size":20,"total":2,"items":[
		{"id":"0199a1b2-0000-7000-8000-0000000000e1","occurred_at":"2026-03-10T09:12:00Z",
		 "actor":{"id":"0199a1b2-0000-7000-8000-00000000000a","name":"Jane Smith"},
		 "company_id":"0199a1b2-0000-7000-8000-00000000000c",
		 "action":"truck.update","entity":"truck","entity_id":"t1",
		 "changes":{"license_plate":{"old":"ABC","new":"XYZ"}},"request_id":"req-1"},
		{"id":"0199a1b2-0000-7000-8000-0000000000e1","occurred_at":"2024-01-15T08:00:00Z",
		 "actor":null,"company_id":null,"action":"user.create","entity":"user","entity_id":"u1","changes":null}
	]}`, rec.Body.String())
}

func TestAuditLogHandler_InvalidQuery(t *testing.T) {
	for _, q := range []string{"actor_id=nope", "from=yesterday", "to=2026-13-01", "page=x", "company_id=1"} {
		rec := do(newAuditRouter(&fakeAuditLogService{}), http.MethodGet, "/api/v1/audit-logs?"+q, "")

		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
		assert.Equal(t, apierror.CodeValidation, decodeError(t, rec).Code, q)
	}
}
