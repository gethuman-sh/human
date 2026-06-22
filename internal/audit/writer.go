package audit

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// writerBufSize is the capacity of the async write channel. Events are dropped
// (with a log warning) when the channel is full so a slow SQLite never
// back-pressures the daemon's command-execution path.
const writerBufSize = 1024

// Writer accepts audit events on a channel and inserts them into a Store in a
// single background goroutine. Call Close to drain remaining events and shut
// down.
type Writer struct {
	ch       chan Event
	store    *Store
	logger   zerolog.Logger
	done     chan struct{}
	quit     chan struct{}
	quitOnce sync.Once
}

// NewWriter creates a Writer and starts the background drain goroutine. The
// goroutine runs until ctx is cancelled or Close is called.
func NewWriter(ctx context.Context, store *Store, logger zerolog.Logger) *Writer {
	w := &Writer{
		ch:     make(chan Event, writerBufSize),
		store:  store,
		logger: logger,
		done:   make(chan struct{}),
		quit:   make(chan struct{}),
	}
	go w.run(ctx)
	return w
}

// Send enqueues an event for async persistence. If the channel is full the
// event is dropped with a warning rather than blocking the caller. The data
// channel is never closed, so Send is safe to call concurrently with — and
// after — Close without panicking; the quit case discards events once shut down.
func (w *Writer) Send(e Event) {
	select {
	case w.ch <- e:
	case <-w.quit:
	default:
		w.logger.Warn().Msg("audit writer channel full, dropping event")
	}
}

func (w *Writer) run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case e := <-w.ch:
			w.insert(ctx, e)
		case <-ctx.Done():
			w.drain()
			return
		case <-w.quit:
			w.drain()
			return
		}
	}
}

// drain inserts any buffered events without blocking. The channel is never
// closed, so the default case (not a closed-channel receive) bounds the loop —
// avoiding the busy-spin a closed channel would cause.
func (w *Writer) drain() {
	for {
		select {
		case e := <-w.ch:
			w.insert(context.Background(), e)
		default:
			return
		}
	}
}

// insert wraps the store call so the timestamp fallback and error logging live
// in one place.
func (w *Writer) insert(ctx context.Context, e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if err := w.store.Insert(ctx, e); err != nil {
		w.logger.Warn().Err(err).Msg("failed to persist audit event")
	}
}

// Close signals the writer to stop and waits for the background goroutine to
// finish draining. It is idempotent and safe to call after ctx cancellation
// has already stopped the goroutine.
func (w *Writer) Close() {
	w.quitOnce.Do(func() { close(w.quit) })
	<-w.done
}
