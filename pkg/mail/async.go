package mail

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrQueueFull: too many emails waiting; this one was dropped.
var ErrQueueFull = errors.New("mail: queue is full")

// Async queues messages and delivers them one by one in the background.
//
// Why: talking to Gmail takes hundreds of milliseconds. Waiting for it
// would slow the request, and for "forgot password" it would also leak
// whether the email exists (only then is a message sent, so only then is
// the answer slow).
//
// The queue lives in memory: emails still queued when the process is
// killed are lost (Close waits for them on a normal shutdown). For a reset
// code that is acceptable, the user can ask again. Mail that must never be
// lost needs a database-backed queue ("outbox").
type Async struct {
	next    Sender
	log     *slog.Logger
	timeout time.Duration // per message

	mu     sync.RWMutex // guards closed and sending on queue
	closed bool
	queue  chan Message
	done   chan struct{}
}

func NewAsync(next Sender, log *slog.Logger, queueSize int, timeout time.Duration) *Async {
	a := &Async{
		next: next, log: log, timeout: timeout,
		queue: make(chan Message, queueSize),
		done:  make(chan struct{}),
	}
	go a.run()
	return a
}

// Send queues m and returns at once. It never blocks: when the queue is
// full, m is dropped and ErrQueueFull returned.
func (a *Async) Send(_ context.Context, m Message) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return errors.New("mail: sender is closed")
	}
	select {
	case a.queue <- m:
		return nil
	default:
		return ErrQueueFull
	}
}

func (a *Async) run() {
	defer close(a.done)
	for m := range a.queue {
		// Not the request's context: the request finished long ago.
		ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
		if err := a.next.Send(ctx, m); err != nil {
			a.log.Error("email delivery failed", "to", m.To, "subject", m.Subject, "error", err)
		}
		cancel()
	}
}

// Close stops accepting messages and waits until the queued ones are sent,
// or ctx ends.
func (a *Async) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()

	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
