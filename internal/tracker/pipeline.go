package tracker

import (
	"context"
	"time"
)

// Optional capabilities for a backend that understands the pipeline's own
// protocol rather than carrying it as opaque comments. Comments stay the wire —
// every marker is still a comment on the ticket — but a backend that indexes,
// validates or announces them lets the daemon read one row instead of parsing a
// thread, and lets a test drive the real machine against a real store. A
// backend that implements none of these behaves exactly as before; the daemon
// asserts for each capability and falls back to the comment thread.

// IndexedMarker is one [human:*] comment as an indexing backend stored it.
type IndexedMarker struct {
	CommentID string            `json:"comment_id"`
	Type      string            `json:"type"`
	Head      string            `json:"head,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	Author    string            `json:"author"`
	Created   time.Time         `json:"created"`
}

// MarkerIndexer answers "which markers does this ticket carry, in order"
// without a comment fetch and a parse per refresh.
type MarkerIndexer interface {
	ListMarkers(ctx context.Context, key string) ([]IndexedMarker, error)
}

// Event is one recorded change to a ticket: what happened, who did it, when.
type Event struct {
	ID      int64     `json:"id"`
	Key     string    `json:"key"`
	Kind    string    `json:"kind"`
	Actor   string    `json:"actor"`
	Detail  string    `json:"detail,omitempty"`
	Created time.Time `json:"created"`
}

// EventLister returns a ticket's history as it was recorded, oldest first.
type EventLister interface {
	ListEvents(ctx context.Context, key string) ([]Event, error)
}

// ChangeCursor lets a poll ask "did anything change" for the price of one
// token instead of a full listing. Equal tokens mean nothing changed; the
// token is otherwise opaque.
type ChangeCursor interface {
	Version(ctx context.Context) (string, error)
}

// PlacementRecorder lets the daemon write back the board placement it derived
// for a ticket, so a backend can show where the work is. The placement is
// derived, never authoritative: the daemon derives it from the markers on every
// fetch and the backend only remembers the latest answer.
type PlacementRecorder interface {
	RecordPlacement(ctx context.Context, key, stage, state string) error
}

// Attribute names a backend reports in Issue.Attributes when it knows them.
const (
	// AttrKind is the ticket's classification: task, feature, bug, security, idea.
	AttrKind = "kind"
	// AttrMaturity is how far the ticket has matured: idea, pm, planned.
	AttrMaturity = "maturity"
	// AttrStage and AttrState are the board placement the daemon last derived.
	AttrStage = "stage"
	AttrState = "state"
)
