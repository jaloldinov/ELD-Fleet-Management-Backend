package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jaloldinov/ELD-learning/internal/model"
)

type fakeRBACSyncer struct {
	gotRoles []model.RoleDef
	gotPerms []model.PermissionDef
	res      *model.RBACSyncResult
	err      error
}

func (f *fakeRBACSyncer) Sync(_ context.Context, roles []model.RoleDef, perms []model.PermissionDef) (*model.RBACSyncResult, error) {
	f.gotRoles, f.gotPerms = roles, perms
	return f.res, f.err
}

func TestSyncRBAC_PassesTheRegistry(t *testing.T) {
	f := &fakeRBACSyncer{res: &model.RBACSyncResult{}}

	require.NoError(t, SyncRBAC(context.Background(), f, slog.New(slog.DiscardHandler)))

	assert.Equal(t, model.SystemRoles(), f.gotRoles)
	assert.Equal(t, model.Permissions(), f.gotPerms)
}

func TestSyncRBAC_LogsChangesAndOrphans(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRBACSyncer{res: &model.RBACSyncResult{
		PermissionsAdded: []model.Permission{"report:read"}, GrantsAdded: 3, Orphaned: []string{"old:thing"},
	}}

	require.NoError(t, SyncRBAC(context.Background(), f, slog.New(slog.NewTextHandler(&buf, nil))))

	assert.Contains(t, buf.String(), "roles and permissions synced")
	assert.Contains(t, buf.String(), "report:read")
	assert.Contains(t, buf.String(), "level=WARN")
	assert.Contains(t, buf.String(), "old:thing")
}

func TestSyncRBAC_QuietWhenNothingChanged(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRBACSyncer{res: &model.RBACSyncResult{}}

	require.NoError(t, SyncRBAC(context.Background(), f, slog.New(slog.NewTextHandler(&buf, nil))))

	assert.Empty(t, buf.String(), "a normal restart logs nothing")
}

func TestSyncRBAC_Error(t *testing.T) {
	f := &fakeRBACSyncer{err: errors.New("db down")}

	assert.Error(t, SyncRBAC(context.Background(), f, slog.New(slog.DiscardHandler)))
}
