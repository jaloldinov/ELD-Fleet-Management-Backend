package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/requestid"
)

// AuditStore saves and reads audit entries (repository.AuditRepository).
type AuditStore interface {
	Insert(ctx context.Context, e model.AuditEntry) error
	List(ctx context.Context, f model.AuditFilter, limit, offset int) ([]model.AuditEntry, int, error)
}

// Auditor records changes in the audit log. Every write in a service uses
// the same pattern:
//
//	err := s.audit.WithinTx(ctx, func(ctx context.Context) error {
//		// ... make the change ...
//		return s.audit.Record(ctx, p, model.AuditEntry{...})
//	})
//
// so the change and its audit row are committed together, or not at all.
type Auditor struct {
	tx    Transactor
	store AuditStore
}

func NewAuditor(tx Transactor, store AuditStore) *Auditor {
	return &Auditor{tx: tx, store: store}
}

// WithinTx runs fn in a transaction (joining the caller's, if any).
func (a *Auditor) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return a.tx.WithinTx(ctx, fn)
}

// Record saves e as done by p. Call it inside WithinTx, after the change.
// The actor comes from p (the system principal is stored as "no actor"),
// the request ID from ctx.
func (a *Auditor) Record(ctx context.Context, p auth.Principal, e model.AuditEntry) error {
	if p.UserID != uuid.Nil {
		id := p.UserID
		e.ActorID = &id
	}
	e.RequestID = requestid.FromContext(ctx)
	return a.store.Insert(ctx, e)
}

// ---------- reading ----------

type AuditLogListParams struct {
	CompanyID *uuid.UUID // platform users only
	Entity    string
	EntityID  string
	ActorID   *uuid.UUID
	Action    string
	From, To  *time.Time
	Page      int
	PageSize  int
}

type AuditLogPage struct {
	Items    []model.AuditEntry
	Total    int
	Page     int
	PageSize int
}

// AuditLogService reads the audit log, within the caller's tenant.
type AuditLogService struct {
	store AuditStore
}

func NewAuditLogService(store AuditStore) *AuditLogService {
	return &AuditLogService{store: store}
}

// List returns entries newest first. Company users only see entries about
// their own company's records; platform users see everything (or filter
// by company).
func (s *AuditLogService) List(ctx context.Context, p auth.Principal, params AuditLogListParams) (*AuditLogPage, error) {
	page, size := normalizePage(ListParams{Page: params.Page, PageSize: params.PageSize})
	f := model.AuditFilter{
		CompanyID: params.CompanyID, Entity: params.Entity, EntityID: params.EntityID,
		ActorID: params.ActorID, Action: params.Action, From: params.From, To: params.To,
	}
	if !p.IsPlatformUser() {
		f.CompanyID = p.CompanyID // tenant scope, applied in SQL
	}
	items, total, err := s.store.List(ctx, f, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	return &AuditLogPage{Items: items, Total: total, Page: page, PageSize: size}, nil
}
