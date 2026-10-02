package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeExpiredDeleter struct {
	gotBefore time.Time
	gotBatch  int
}

func (f *fakeExpiredDeleter) DeleteExpired(_ context.Context, before time.Time, batch int) (int64, error) {
	f.gotBefore, f.gotBatch = before, batch
	return 42, nil
}

func TestSessionCleaner_KeepsRetentionPeriod(t *testing.T) {
	repo := &fakeExpiredDeleter{}
	c := NewSessionCleaner(repo, 7*24*time.Hour)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	n, err := c.Run(context.Background())

	require.NoError(t, err)
	assert.EqualValues(t, 42, n)
	assert.Equal(t, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), repo.gotBefore, "tokens expired in the last 7 days are kept")
	assert.Equal(t, sessionCleanupBatchSize, repo.gotBatch)
}
