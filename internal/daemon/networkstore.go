package daemon

import (
	"sync"
	"time"

	client "github.com/gethuman-sh/human-daemon-client"
)

// maxNetworkEvents bounds the total number of deduplicated network
// event rows kept in memory. This is a display buffer, not an audit
// log: a small cap keeps memory predictable under hostile bursts and
// still dwarfs the maximum number of rows any terminal can render.
const maxNetworkEvents = 200

// NetworkEvent is a single ambient network activity row as rendered by
// the TUI activity panel. Consecutive events with the same Host and
// Source are collapsed into one row with an incrementing Count and a
// refreshed LastSeen timestamp. The struct is defined by the public
// human-daemon-client contract so the TUI can render it through that module;
// the daemon aliases it and the store constructs it.
type NetworkEvent = client.NetworkEvent

// NetworkEventStore is a thread-safe ring buffer of recent network
// events with consecutive-host deduplication. It models
// HookEventStore but collapses bursts at write time so the panel stays
// calm under sustained repeats.
type NetworkEventStore struct {
	mu     sync.Mutex
	events []NetworkEvent
	nowFn  func() time.Time
}

// NewNetworkEventStore creates an empty store using time.Now as its
// clock. Tests can override the clock via NewNetworkEventStoreWithClock.
func NewNetworkEventStore() *NetworkEventStore {
	return &NetworkEventStore{
		events: make([]NetworkEvent, 0, maxNetworkEvents),
		nowFn:  func() time.Time { return time.Now().UTC() },
	}
}

// NewNetworkEventStoreWithClock is the test constructor that injects a
// deterministic clock so dedup timestamps are reproducible.
func NewNetworkEventStoreWithClock(nowFn func() time.Time) *NetworkEventStore {
	return &NetworkEventStore{
		events: make([]NetworkEvent, 0, maxNetworkEvents),
		nowFn:  nowFn,
	}
}

// Emit satisfies proxy.NetworkEventEmitter. Consecutive events with the
// same (source, host) pair are collapsed into the tail row: Count is
// incremented and LastSeen is refreshed. A different host or source
// starts a new row, even if the host was seen earlier in the buffer.
// Collapsing at write time keeps memory flat under sustained bursts so
// the panel stays calm under noise.
func (s *NetworkEventStore) Emit(source, status, host string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFn()
	if n := len(s.events); n > 0 {
		tail := &s.events[n-1]
		if tail.Source == source && tail.Host == host {
			tail.Count++
			tail.LastSeen = now
			// Status of a collapsed row reflects the most recent event
			// so a forward→block transition on the same tail surfaces
			// even within the collapsed window. In practice consecutive
			// same-host events almost always share a status.
			tail.Status = status
			return
		}
	}

	s.events = append(s.events, NetworkEvent{
		Source:   source,
		Status:   status,
		Host:     host,
		Count:    1,
		LastSeen: now,
	})
	if len(s.events) > maxNetworkEvents {
		// Drop the oldest entries from the front; the display shows
		// newest-first anyway, so the oldest rows are the ones the
		// user is least likely to miss.
		copy(s.events, s.events[len(s.events)-maxNetworkEvents:])
		s.events = s.events[:maxNetworkEvents]
	}
}

// Snapshot returns a copy of all current rows in insertion order
// (oldest first). Callers that want newest-first should reverse the
// slice; the store keeps events in insertion order so testing and
// JSON serialization remain predictable.
func (s *NetworkEventStore) Snapshot() []NetworkEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]NetworkEvent, len(s.events))
	copy(out, s.events)
	return out
}
