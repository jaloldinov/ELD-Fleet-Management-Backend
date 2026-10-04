package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/service"
	"github.com/jaloldinov/ELD-learning/pkg/validation"
)

// AuditLogService is what AuditLogHandler needs from the service layer.
type AuditLogService interface {
	List(ctx context.Context, p auth.Principal, params service.AuditLogListParams) (*service.AuditLogPage, error)
}

// ---------- DTOs ----------

type AuditActor struct {
	ID   uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	Name string    `json:"name" example:"Jane Smith"`
}

type AuditChangeResponse struct {
	Old any `json:"old"`
	New any `json:"new"`
}

type AuditLogResponse struct {
	ID         uuid.UUID `json:"id" swaggertype:"string" format:"uuid"`
	OccurredAt time.Time `json:"occurred_at" example:"2026-10-03T11:26:26Z"`
	// null = the system itself (e.g. the createadmin CLI).
	Actor     *AuditActor `json:"actor"`
	CompanyID *uuid.UUID  `json:"company_id" swaggertype:"string" format:"uuid"`
	Action    string      `json:"action" example:"truck.update"`
	Entity    string      `json:"entity" example:"truck"`
	EntityID  string      `json:"entity_id" example:"0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"`
	// Only the fields that changed: {"unit_number": {"old": "101", "new": "101A"}}.
	// null for actions without values (delete, password changes).
	Changes   map[string]AuditChangeResponse `json:"changes"`
	RequestID string                         `json:"request_id,omitempty" example:"9c1a1ea6-d4cb-4085-a373-493ea2077a6c"`
}

func newAuditLogResponse(e *model.AuditEntry) AuditLogResponse {
	r := AuditLogResponse{
		ID: e.ID, OccurredAt: e.OccurredAt.UTC(), CompanyID: e.CompanyID,
		Action: e.Action, Entity: e.Entity, EntityID: e.EntityID, RequestID: e.RequestID,
	}
	if e.ActorID != nil {
		r.Actor = &AuditActor{ID: *e.ActorID, Name: e.ActorName}
	}
	if e.Changes != nil {
		r.Changes = make(map[string]AuditChangeResponse, len(e.Changes))
		for field, c := range e.Changes {
			r.Changes[field] = AuditChangeResponse{Old: c.Old, New: c.New}
		}
	}
	return r
}

type ListAuditLogsResponse struct {
	Items    []AuditLogResponse `json:"items"`
	Page     int                `json:"page" example:"1"`
	PageSize int                `json:"page_size" example:"20"`
	Total    int                `json:"total" example:"3"`
}

// ---------- Handler ----------

type AuditLogHandler struct {
	svc AuditLogService
}

func NewAuditLogHandler(svc AuditLogService) *AuditLogHandler {
	return &AuditLogHandler{svc: svc}
}

// List handles GET /api/v1/audit-logs.
//
//	@Summary		List audit log entries
//	@Description	Requires permission: audit:read
//	@Description	Who changed what and when, newest first. Company users see only their own company's records (company_id is ignored).
//	@Description	For one record's history (e.g. a timeline in the UI), filter by entity and entity_id.
//	@Tags			audit
//	@Produce		json
//	@Security		BearerAuth
//	@Param			entity		query		string	false	"Entity type"	Enums(company, user, driver, truck, eld_device, rate_limit)
//	@Param			entity_id	query		string	false	"ID of one record (with entity)"
//	@Param			action		query		string	false	"Exact action, e.g. truck.update"
//	@Param			actor_id	query		string	false	"Who made the change"				format(uuid)
//	@Param			company_id	query		string	false	"Platform users only"				format(uuid)
//	@Param			from		query		string	false	"Changes at or after (RFC 3339)"	format(date-time)
//	@Param			to			query		string	false	"Changes before (RFC 3339)"			format(date-time)
//	@Param			page		query		int		false	"Page number (1-based)"				default(1)	minimum(1)
//	@Param			page_size	query		int		false	"Items per page"					default(20)	minimum(1)	maximum(100)
//	@Success		200			{object}	ListAuditLogsResponse
//	@Failure		400			{object}	apierror.Response
//	@Failure		401			{object}	apierror.Response	"missing or invalid access token"
//	@Failure		403			{object}	apierror.Response	"missing permission audit:read"
//	@Failure		500			{object}	apierror.Response
//	@Router			/api/v1/audit-logs [get]
func (h *AuditLogHandler) List(c *gin.Context) {
	p, ok := principal(c)
	if !ok {
		return
	}
	var v validation.Validator
	page, ok := queryInt(c, "page")
	v.Check(ok, "page", "must be an integer")
	size, ok := queryInt(c, "page_size")
	v.Check(ok, "page_size", "must be an integer")
	actorID, ok := queryUUID(c, "actor_id")
	v.Check(ok, "actor_id", "must be a valid UUID")
	companyID, ok := queryUUID(c, "company_id")
	v.Check(ok, "company_id", "must be a valid UUID")
	from, ok := queryTime(c, "from")
	v.Check(ok, "from", "must be an RFC 3339 time, e.g. 2026-10-01T00:00:00Z")
	to, ok := queryTime(c, "to")
	v.Check(ok, "to", "must be an RFC 3339 time, e.g. 2026-10-01T00:00:00Z")
	if err := v.Err(); err != nil {
		handleError(c, err)
		return
	}

	res, err := h.svc.List(c.Request.Context(), p, service.AuditLogListParams{
		CompanyID: companyID, Entity: c.Query("entity"), EntityID: c.Query("entity_id"),
		ActorID: actorID, Action: c.Query("action"), From: from, To: to,
		Page: page, PageSize: size,
	})
	if err != nil {
		handleError(c, err)
		return
	}
	items := make([]AuditLogResponse, 0, len(res.Items))
	for i := range res.Items {
		items = append(items, newAuditLogResponse(&res.Items[i]))
	}
	c.JSON(http.StatusOK, ListAuditLogsResponse{Items: items, Page: res.Page, PageSize: res.PageSize, Total: res.Total})
}

// queryTime reads an optional RFC 3339 query parameter. A missing parameter
// returns nil; ok is false only for invalid values.
func queryTime(c *gin.Context, key string) (t *time.Time, ok bool) {
	s := c.Query(key)
	if s == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, false
	}
	return &parsed, true
}
