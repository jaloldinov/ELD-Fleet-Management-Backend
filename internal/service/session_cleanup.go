package service

import (
	"context"
	"time"
)

// ExpiredSessionDeleter is the repository method the cleaner needs.
type ExpiredSessionDeleter interface {
	DeleteExpired(ctx context.Context, before time.Time, batchSize int) (int64, error)
}

const sessionCleanupBatchSize = 5000

// SessionCleaner permanently deletes refresh tokens that expired more than
// `retention` ago. Expired tokens are useless for logging in (even a stolen
// one is rejected), but are kept a little longer to investigate incidents.
type SessionCleaner struct {
	repo      ExpiredSessionDeleter
	retention time.Duration
	now       func() time.Time
}

func NewSessionCleaner(repo ExpiredSessionDeleter, retention time.Duration) *SessionCleaner {
	return &SessionCleaner{repo: repo, retention: retention, now: time.Now}
}

// Run deletes everything past retention and returns how many tokens it deleted.
func (c *SessionCleaner) Run(ctx context.Context) (int64, error) {
	return c.repo.DeleteExpired(ctx, c.now().Add(-c.retention), sessionCleanupBatchSize)
}
