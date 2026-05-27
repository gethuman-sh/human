package stats

import (
	"context"
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
	ch     chan hookevents.Event
	store  *StatsStore
	logger zerolog.Logger
	done   chan struct{}
}

// NewWriter creates a Writer and starts the background drain goroutine.
// The goroutine runs until ctx is cancelled or Close is called.
func NewWriter(ctx context.Context, store *StatsStore, logger zerolog.Logger) *Writer {
	w := &Writer{
		ch:     make(chan hookevents.Event, writerBufSize),
		store:  store,
		logger: logger,
		done:   make(chan struct{}),
	}
	go w.run(ctx)
	return w
}

// Send enqueues an event for async persistence. If the channel is full
// the event is dropped silently (trends tolerate small gaps).
func (w *Writer) Send(evt hookevents.Event) {
	select {
	case w.ch <- evt:
	default:
		w.logger.Warn().Msg("stats writer channel full, dropping event")
	}
}

func (w *Writer) run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case evt, ok := <-w.ch:
			if !ok {
				return
			}
			w.insert(ctx, evt)
		case <-ctx.Done():
			// Drain remaining buffered events before exiting.
			for {
				select {
				case evt := <-w.ch:
					w.insert(context.Background(), evt)
				default:
					return
				}
			}
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

// Close signals the writer to stop and waits for the background goroutine
// to finish draining.
func (w *Writer) Close() {
	close(w.ch)
	<-w.done
}
