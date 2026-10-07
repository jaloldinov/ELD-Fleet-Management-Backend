package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripeEventRepository_OncePerEvent(t *testing.T) {
	db := newTestDB(t)
	repo := NewStripeEventRepository(db)
	ctx := context.Background()

	done, err := repo.Start(ctx, "evt_1", "invoice.paid")
	require.NoError(t, err)
	assert.False(t, done, "new event: process it")

	// The first attempt failed: Stripe's retry must process it again.
	require.NoError(t, repo.MarkFailed(ctx, "evt_1", "stripe is down"))
	done, err = repo.Start(ctx, "evt_1", "invoice.paid")
	require.NoError(t, err)
	assert.False(t, done, "failed before: process again")

	require.NoError(t, repo.MarkProcessed(ctx, "evt_1"))
	done, err = repo.Start(ctx, "evt_1", "invoice.paid")
	require.NoError(t, err)
	assert.True(t, done, "processed: a repeated delivery is skipped")

	var attempts int
	var lastError *string
	require.NoError(t, db.QueryRow(ctx, `SELECT attempts, last_error FROM stripe_events WHERE id = 'evt_1'`).
		Scan(&attempts, &lastError))
	assert.Equal(t, 3, attempts)
	assert.Nil(t, lastError, "cleared once processed")

	_, err = repo.Start(ctx, "not-an-event", "x")
	assert.Error(t, err, "ids must look like Stripe's")
}
