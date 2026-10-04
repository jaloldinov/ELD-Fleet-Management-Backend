package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

// AuditRepository writes and reads the audit log. It can only add rows: the
// table rejects UPDATE and DELETE (see migration 000013).
type AuditRepository struct {
	db DBTX
}

func NewAuditRepository(db DBTX) *AuditRepository {
	return &AuditRepository{db: db}
}

// q joins the caller's transaction: an audit row is committed (or rolled
// back) together with the change it describes.
func (r *AuditRepository) q(ctx context.Context) DBTX {
	return conn(ctx, r.db)
}

// changeJSON is how one field's change is stored: {"old": ..., "new": ...}.
type changeJSON struct {
	Old any `json:"old"`
	New any `json:"new"`
}

func (r *AuditRepository) Insert(ctx context.Context, e model.AuditEntry) error {
	var changes []byte // nil → SQL NULL
	if len(e.Changes) > 0 {
		m := make(map[string]changeJSON, len(e.Changes))
		for field, c := range e.Changes {
			m[field] = changeJSON{Old: c.Old, New: c.New}
		}
		var err error
		if changes, err = json.Marshal(m); err != nil {
			return fmt.Errorf("encode audit changes: %w", err)
		}
	}
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO audit_logs (actor_id, company_id, action, entity, entity_id, changes, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))`,
		e.ActorID, e.CompanyID, e.Action, e.Entity, e.EntityID, changes, e.RequestID)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// List returns entries newest first, with the actor's current name.
func (r *AuditRepository) List(ctx context.Context, f model.AuditFilter, limit, offset int) ([]model.AuditEntry, int, error) {
	const where = `
		WHERE ($1::uuid IS NULL OR a.company_id = $1)
		  AND ($2 = '' OR a.entity = $2)
		  AND ($3 = '' OR a.entity_id = $3)
		  AND ($4::uuid IS NULL OR a.actor_id = $4)
		  AND ($5 = '' OR a.action = $5)
		  AND ($6::timestamptz IS NULL OR a.occurred_at >= $6)
		  AND ($7::timestamptz IS NULL OR a.occurred_at < $7)`
	args := []any{f.CompanyID, f.Entity, f.EntityID, f.ActorID, f.Action, f.From, f.To}

	var total int
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM audit_logs a`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit entries: %w", err)
	}

	rows, err := r.q(ctx).Query(ctx, `
		SELECT a.id, a.occurred_at, a.actor_id, coalesce(u.first_name || ' ' || u.last_name, ''),
		       a.company_id, a.action, a.entity, a.entity_id, a.changes, coalesce(a.request_id, '')
		FROM audit_logs a
		LEFT JOIN users u ON u.id = a.actor_id`+where+`
		ORDER BY a.occurred_at DESC, a.id DESC
		LIMIT $8 OFFSET $9`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit entries: %w", err)
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.AuditEntry, error) {
		var e model.AuditEntry
		var changes []byte
		if err := row.Scan(&e.ID, &e.OccurredAt, &e.ActorID, &e.ActorName, &e.CompanyID,
			&e.Action, &e.Entity, &e.EntityID, &changes, &e.RequestID); err != nil {
			return e, err
		}
		if changes != nil {
			var m map[string]changeJSON
			if err := json.Unmarshal(changes, &m); err != nil {
				return e, fmt.Errorf("decode audit changes: %w", err)
			}
			e.Changes = make(model.AuditChanges, len(m))
			for field, c := range m {
				e.Changes[field] = model.AuditChange{Old: c.Old, New: c.New}
			}
		}
		return e, nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("scan audit entries: %w", err)
	}
	return entries, total, nil
}
