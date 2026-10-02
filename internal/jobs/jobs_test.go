package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEvery_RunsImmediatelyRepeatsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	done := make(chan struct{})

	go func() {
		Every(ctx, slog.New(slog.DiscardHandler), "test", 5*time.Millisecond, func(context.Context) error {
			if runs.Add(1) == 3 {
				cancel()
			}
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Every did not stop after cancel")
	}
	assert.Equal(t, int32(3), runs.Load())
}

func TestEvery_ErrorIsLoggedAndJobKeepsRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	var runs atomic.Int32

	Every(ctx, slog.New(slog.NewTextHandler(&buf, nil)), "cleanup", time.Millisecond, func(context.Context) error {
		if runs.Add(1) == 2 {
			cancel()
		}
		return errors.New("db down")
	})

	assert.Equal(t, int32(2), runs.Load(), "a failure does not stop the job")
	assert.Contains(t, buf.String(), "job=cleanup")
	assert.Contains(t, buf.String(), "db down")
}
