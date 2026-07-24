package daemon

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

// maxHookEventsPerSession caps per-session buffered events so a burst
// from one session cannot evict other sessions from the ring. The
// aggregate size stays bounded through maxHookEvents.
const maxHookEventsPerSession = 200

// maxHookEvents bounds the total number of events across all
// sessions. Raised from the previous 100-event global ring because a
// busy agent produces ~60 events per 30 tool calls and easily
// overflows that bound before reaching visible pruning behaviour.
const maxHookEvents = 10000

// HookEventStore is a thread-safe ring buffer of recent hook events.
// It stores raw events and can derive per-session snapshots on demand.
// Subscribers are notified (non-blocking) whenever a new event is appended.
type HookEventStore struct {
	mu          sync.Mutex
	events      []hookevents.Event
	eventSeqs   []uint64 // monotonic id per event, index-aligned with events
	appended    uint64   // total events ever appended (last assigned sequence)
	subscribers []chan struct{}
	// progress is the last sign of life per agent, kept outside the ring so
	// eviction cannot make a quiet-but-working agent look hung.
	progress map[string]AgentProgress
	// persist is an optional durable sink invoked for every appended event.
	// The in-memory ring evicts under load and is empty after a restart, so a
	// hook event tied to a since-reaped agent run is otherwise lost; the sink
	// writes it to the host so the trail survives. nil = memory-only.
	persist func(hookevents.Event)
}

// NewHookEventStore creates an empty store.
func NewHookEventStore() *HookEventStore {
	return &HookEventStore{
		events:    make([]hookevents.Event, 0, maxHookEvents),
		eventSeqs: make([]uint64, 0, maxHookEvents),
		progress:  make(map[string]AgentProgress),
	}
}

// WithPersistence sets a durable sink invoked for every appended event and
// returns the store for chaining. Wire this only on the daemon's production
// store; tests keep the default memory-only behaviour.
func (s *HookEventStore) WithPersistence(sink func(hookevents.Event)) *HookEventStore {
	s.mu.Lock()
	s.persist = sink
	s.mu.Unlock()
	return s
}

// Append adds a hook event. Before appending, events for the same
// session beyond maxHookEventsPerSession are dropped so one chatty
// client cannot push other sessions out of the shared buffer. The
// aggregate cap then drops the oldest events across all sessions.
func (s *HookEventStore) Append(evt hookevents.Event) {
	s.mu.Lock()
	// Per-session eviction: count existing events for this session and
	// drop the oldest when the session cap is exceeded.
	if evt.SessionID != "" {
		sessionCount := 0
		for _, e := range s.events {
			if e.SessionID == evt.SessionID {
				sessionCount++
			}
		}
		if sessionCount >= maxHookEventsPerSession {
			for i, e := range s.events {
				if e.SessionID == evt.SessionID {
					s.events = append(s.events[:i], s.events[i+1:]...)
					s.eventSeqs = append(s.eventSeqs[:i], s.eventSeqs[i+1:]...)
					break
				}
			}
		}
	}
	if s.progress == nil {
		s.progress = make(map[string]AgentProgress)
	}
	trackProgress(s.progress, evt)
	s.appended++
	s.events = append(s.events, evt)
	s.eventSeqs = append(s.eventSeqs, s.appended)
	if len(s.events) > maxHookEvents {
		copy(s.events, s.events[len(s.events)-maxHookEvents:])
		s.events = s.events[:maxHookEvents]
		copy(s.eventSeqs, s.eventSeqs[len(s.eventSeqs)-maxHookEvents:])
		s.eventSeqs = s.eventSeqs[:maxHookEvents]
	}
	subs := make([]chan struct{}, len(s.subscribers))
	copy(subs, s.subscribers)
	persist := s.persist
	s.mu.Unlock()

	// Persist outside the mutex so slow disk I/O never holds up other appends.
	if persist != nil {
		persist(evt)
	}

	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default: // non-blocking — subscriber already has a pending notification
		}
	}
}

// AgentProgress returns the last progress seen from an agent. ok is false when
// nothing is known — the daemon restarted, or the agent has not acted yet.
func (s *HookEventStore) AgentProgress(agentName string) (AgentProgress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.progress[agentName]
	return p, ok
}

// Subscribe returns a channel that receives a signal whenever a new event is
// appended. The channel has a buffer of 1 so a single pending notification is
// coalesced. Call Unsubscribe to clean up.
func (s *HookEventStore) Subscribe() chan struct{} {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers = append(s.subscribers, ch)
	s.mu.Unlock()
	return ch
}

// Unsubscribe removes a previously registered channel from the subscriber
// list. The channel is not closed — subscribers must stop reading from it
// after calling Unsubscribe and let it be garbage collected. This avoids
// coordinating with any concurrent Append on a removed channel.
func (s *HookEventStore) Unsubscribe(ch chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, sub := range s.subscribers {
		if sub == ch {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			return
		}
	}
}

// Snapshot returns the current per-session state derived from all stored events.
func (s *HookEventStore) Snapshot() map[string]hookevents.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions := make(map[string]hookevents.SessionSnapshot)
	for _, evt := range s.events {
		if evt.SessionID == "" {
			continue
		}
		snap := sessions[evt.SessionID]
		snap.SessionID = evt.SessionID
		if evt.Cwd != "" {
			snap.Cwd = evt.Cwd
		}
		snap.LastEventAt = evt.Timestamp
		hookevents.ApplyEvent(&snap, &evt)
		sessions[evt.SessionID] = snap
	}
	return sessions
}

// RecentEvents returns a copy of all stored events.
func (s *HookEventStore) RecentEvents() []hookevents.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hookevents.Event, len(s.events))
	copy(out, s.events)
	return out
}

// EventsSince returns the events appended after the given sequence, plus the
// current high-water sequence to pass on the next call. Sequences are
// monotonic and independent of the ring's length, so a subscriber keeps
// receiving new events even after the ring saturates and stops growing — the
// failure mode of tracking deltas by slice length. Pass 0 for the first call.
func (s *HookEventStore) EventsSince(since uint64) ([]hookevents.Event, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []hookevents.Event
	for i, seq := range s.eventSeqs {
		if seq > since {
			out = append(out, s.events[i])
		}
	}
	return out, s.appended
}

// hookFieldMaxLen bounds every captured hook event field. Without a
// bound a misbehaving client could swamp the in-memory store with
// multi-megabyte strings.
const hookFieldMaxLen = 1024

// clampField truncates s to at most hookFieldMaxLen bytes.
func clampField(s string) string {
	if len(s) > hookFieldMaxLen {
		return s[:hookFieldMaxLen]
	}
	return s
}

// ParseHookEventArgs converts daemon request args into a hook event.
// Expected args: [event, session_id, cwd, notification_type, tool_name, error_type, agent_name].
//
// Every field is length-capped and Cwd must be absolute — both as
// defence against abusive clients that could otherwise poison the
// in-memory hook store or write relative paths that collide with
// registered project directories.
func ParseHookEventArgs(args []string) hookevents.Event {
	evt := hookevents.Event{
		Timestamp: time.Now().UTC(),
	}
	if len(args) > 0 {
		evt.EventName = clampField(args[0])
	}
	if len(args) > 1 {
		evt.SessionID = clampField(args[1])
	}
	if len(args) > 2 {
		cwd := clampField(args[2])
		// Reject non-absolute Cwd so clients cannot inject paths that
		// match registered projects by naming a relative path.
		if cwd != "" && !filepath.IsAbs(cwd) {
			cwd = ""
		}
		evt.Cwd = cwd
	}
	if len(args) > 3 {
		evt.NotificationType = clampField(args[3])
	}
	if len(args) > 4 {
		evt.ToolName = clampField(args[4])
	}
	if len(args) > 5 {
		evt.ErrorType = clampField(args[5])
	}
	if len(args) > 6 {
		evt.AgentName = clampField(args[6])
	}
	return evt
}
