package stats

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

// writerBufSize is the capacity of the async write channel. Events are
// dropped (with a log warning) when the channel is full, which prevents
// a slow SQLite from back-pressuring the in-memory hook event path.
const writerBufSize = 1024

// Writer accepts hook events on a channel and inserts them into a StatsStore
// in a single background goroutine. Call Close to drain remaining events and
// shut down.
type Writer struct {
	ch       chan hookevents.Event
	store    *StatsStore
	logger   zerolog.Logger
	done     chan struct{}
	quit     chan struct{}
	quitOnce sync.Once
}

// NewWriter creates a Writer and starts the background drain goroutine.
// The goroutine runs until ctx is cancelled or Close is called.
func NewWriter(ctx context.Context, store *StatsStore, logger zerolog.Logger) *Writer {
	w := &Writer{
		ch:     make(chan hookevents.Event, writerBufSize),
		store:  store,
		logger: logger,
		done:   make(chan struct{}),
		quit:   make(chan struct{}),
	}
	go w.run(ctx)
	return w
}

// Send enqueues an event for async persistence. If the channel is full the
// event is dropped silently (trends tolerate small gaps). The data channel is
// never closed, so Send is safe to call concurrently with — and after — Close
// without panicking; the quit case just discards events once shut down.
func (w *Writer) Send(evt hookevents.Event) {
	select {
	case w.ch <- evt:
	case <-w.quit:
	default:
		w.logger.Warn().Msg("stats writer channel full, dropping event")
	}
}

func (w *Writer) run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case evt := <-w.ch:
			w.insert(ctx, evt)
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
// closed, so the default case (not a closed-channel receive) is what bounds
// the loop — avoiding the busy-spin that a closed channel would cause.
func (w *Writer) drain() {
	for {
		select {
		case evt := <-w.ch:
			w.insert(context.Background(), evt)
		default:
			return
		}
	}
}

// insert wraps the store call so the timestamp fallback and error logging
// live in one place.
func (w *Writer) insert(ctx context.Context, evt hookevents.Event) {
	ts := evt.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if err := w.store.InsertEvent(ctx, evt.SessionID, evt.EventName, evt.ToolName, evt.Cwd, evt.ErrorType, ts); err != nil {
		w.logger.Warn().Err(err).Msg("failed to persist tool event")
	}
}

// Close signals the writer to stop and waits for the background goroutine to
// finish draining. It is idempotent and safe to call after ctx cancellation
// has already stopped the goroutine.
func (w *Writer) Close() {
	w.quitOnce.Do(func() { close(w.quit) })
	<-w.done
}
