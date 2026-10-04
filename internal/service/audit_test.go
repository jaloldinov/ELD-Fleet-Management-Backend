package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/pkg/requestid"
)

// memAuditStore records entries, and whether each was written inside a
// transaction (fakeTx marks the ctx).
type memAuditStore struct {
	entries   []model.AuditEntry
	outsideTx int
	listF     model.AuditFilter
}

func (m *memAuditStore) Insert(ctx context.Context, e model.AuditEntry) error {
	if !inTx(ctx) {
		m.outsideTx++
	}
	m.entries = append(m.entries, e)
	return nil
}

func (m *memAuditStore) List(_ context.Context, f model.AuditFilter, _, _ int) ([]model.AuditEntry, int, error) {
	m.listF = f
	return m.entries, len(m.entries), nil
}

// actions lists the recorded actions, in order.
func (m *memAuditStore) actions() []string {
	out := make([]string, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.Action)
	}
	return out
}

// last returns the newest entry.
func (m *memAuditStore) last(t *testing.T) model.AuditEntry {
	t.Helper()
	require.NotEmpty(t, m.entries, "an audit entry was recorded")
	return m.entries[len(m.entries)-1]
}

// requireAllInTx fails if any entry was written outside a transaction.
func (m *memAuditStore) requireAllInTx(t *testing.T) {
	t.Helper()
	assert.Zero(t, m.outsideTx, "audit entries must be written in the change's transaction")
}

// newTestAuditor returns an auditor backed by fakeTx and a memory store.
func newTestAuditor() (*Auditor, *memAuditStore) {
	store := &memAuditStore{}
	return NewAuditor(&fakeTx{}, store), store
}

func TestAuditor_RecordFillsActorAndRequestID(t *testing.T) {
	a, store := newTestAuditor()
	ctx := requestid.NewContext(context.Background(), "req-42")
	p := auth.Principal{UserID: uuid.New()}

	require.NoError(t, a.WithinTx(ctx, func(ctx context.Context) error {
		return a.Record(ctx, p, model.AuditEntry{Action: "truck.delete", Entity: model.AuditTruck, EntityID: "t1"})
	}))

	e := store.last(t)
	require.NotNil(t, e.ActorID)
	assert.Equal(t, p.UserID, *e.ActorID)
	assert.Equal(t, "req-42", e.RequestID)
	store.requireAllInTx(t)
}

func TestAuditor_SystemPrincipalHasNoActor(t *testing.T) {
	a, store := newTestAuditor()

	require.NoError(t, a.Record(context.Background(), auth.SystemPrincipal(), model.AuditEntry{Action: "user.create"}))

	assert.Nil(t, store.last(t).ActorID)
}

func TestAuditLogService_TenantScope(t *testing.T) {
	store := &memAuditStore{}
	svc := NewAuditLogService(store)
	other := uuid.New()

	_, err := svc.List(context.Background(), companyAUser, AuditLogListParams{CompanyID: &other, Entity: model.AuditTruck})
	require.NoError(t, err)
	assert.Equal(t, &companyA, store.listF.CompanyID, "a company user always gets their own company")
	assert.Equal(t, model.AuditTruck, store.listF.Entity)

	_, err = svc.List(context.Background(), platformAdmin, AuditLogListParams{CompanyID: &other})
	require.NoError(t, err)
	assert.Equal(t, &other, store.listF.CompanyID, "platform users may filter by any company")
}
