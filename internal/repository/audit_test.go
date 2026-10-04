package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

func TestAuditRepository_InsertAndList(t *testing.T) {
	db := newTestDB(t)
	repo := NewAuditRepository(db)
	ctx := context.Background()
	company := createCompany(t, NewCompanyRepository(db), "Acme", "1")
	jane, err := NewUserRepository(db).Create(ctx, newUser(company.ID, "jane@acme.com"), []string{model.RoleAdmin})
	require.NoError(t, err)
	truckID := uuid.NewString()

	require.NoError(t, repo.Insert(ctx, model.AuditEntry{
		ActorID: &jane.ID, CompanyID: &company.ID, Action: "truck.create", Entity: model.AuditTruck, EntityID: truckID,
		Changes: model.AuditChanges{"unit_number": {Old: nil, New: "101"}}, RequestID: "req-1",
	}))
	require.NoError(t, repo.Insert(ctx, model.AuditEntry{
		ActorID: &jane.ID, CompanyID: &company.ID, Action: "truck.update", Entity: model.AuditTruck, EntityID: truckID,
		Changes: model.AuditChanges{"unit_number": {Old: "101", New: "101A"}, "year": {Old: nil, New: 2022}},
	}))
	require.NoError(t, repo.Insert(ctx, model.AuditEntry{ // another truck, by the system
		CompanyID: &company.ID, Action: "truck.delete", Entity: model.AuditTruck, EntityID: uuid.NewString(),
	}))

	got, total, err := repo.List(ctx, model.AuditFilter{Entity: model.AuditTruck, EntityID: truckID}, 10, 0)

	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, got, 2)
	assert.Equal(t, "truck.update", got[0].Action, "newest first")
	assert.Equal(t, "Jane Smith", got[0].ActorName)
	assert.Equal(t, model.AuditChanges{
		"unit_number": {Old: "101", New: "101A"},
		"year":        {Old: nil, New: float64(2022)}, // JSON numbers come back as float64
	}, got[0].Changes)
	assert.Equal(t, "req-1", got[1].RequestID)
	assert.WithinDuration(t, time.Now(), got[0].OccurredAt, time.Minute)

	all, total, err := repo.List(ctx, model.AuditFilter{CompanyID: &company.ID}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Nil(t, all[0].ActorID, "the system")
	assert.Empty(t, all[0].ActorName)
	assert.Nil(t, all[0].Changes)
}

func TestAuditRepository_Filters(t *testing.T) {
	db := newTestDB(t)
	repo := NewAuditRepository(db)
	ctx := context.Background()
	a, b, actor := uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, repo.Insert(ctx, model.AuditEntry{CompanyID: &a, ActorID: &actor, Action: "truck.update", Entity: model.AuditTruck, EntityID: "t1"}))
	require.NoError(t, repo.Insert(ctx, model.AuditEntry{CompanyID: &b, Action: "user.delete", Entity: model.AuditUser, EntityID: "u1"}))

	count := func(f model.AuditFilter) int {
		_, n, err := repo.List(ctx, f, 10, 0)
		require.NoError(t, err)
		return n
	}
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)

	assert.Equal(t, 1, count(model.AuditFilter{CompanyID: &a}), "tenant")
	assert.Equal(t, 1, count(model.AuditFilter{ActorID: &actor}))
	assert.Equal(t, 1, count(model.AuditFilter{Action: "user.delete"}))
	assert.Equal(t, 2, count(model.AuditFilter{From: &past, To: &future}))
	assert.Equal(t, 0, count(model.AuditFilter{From: &future}))
}

// The point of an audit log: nobody can rewrite or erase it.
func TestAuditRepository_AppendOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	require.NoError(t, NewAuditRepository(db).Insert(ctx, model.AuditEntry{Action: "truck.delete", Entity: model.AuditTruck, EntityID: "t1"}))

	_, err := db.Exec(ctx, `UPDATE audit_logs SET action = 'truck.create'`)
	assert.ErrorContains(t, err, "append-only")
	_, err = db.Exec(ctx, `DELETE FROM audit_logs`)
	assert.ErrorContains(t, err, "append-only")
}

// The change and its audit row commit together, or not at all.
func TestAuditRepository_RolledBackWithTheChange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	repo := NewAuditRepository(db)

	err := NewTxManager(db).WithinTx(ctx, func(ctx context.Context) error {
		require.NoError(t, repo.Insert(ctx, model.AuditEntry{Action: "truck.update", Entity: model.AuditTruck, EntityID: "t1"}))
		return errors.New("the change failed")
	})
	require.Error(t, err)

	_, total, err := repo.List(ctx, model.AuditFilter{}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
}

func TestAuditRepository_ActionFormat(t *testing.T) {
	db := newTestDB(t)

	err := NewAuditRepository(db).Insert(context.Background(), model.AuditEntry{Action: "Truck Update", Entity: "truck", EntityID: "t1"})

	assert.Error(t, err)
}
