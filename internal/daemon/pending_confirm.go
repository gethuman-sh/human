package daemon

import (
	"sync"
	"time"

	"github.com/gethuman-sh/human/errors"
)

const confirmTimeout = 5 * time.Minute

// PendingConfirmation represents a destructive operation that is blocked
// waiting for user confirmation via the TUI.
type PendingConfirmation struct {
	ID        string
	Operation string // "DeleteIssue", "EditIssue"
	Tracker   string // tracker kind, e.g. "jira"
	Key       string // issue key, e.g. "KAN-1"
	Prompt    string // human-readable, e.g. "Delete KAN-1?"
	ClientPID int    // PID of the Claude instance that triggered the operation
	CreatedAt time.Time
	Decision  chan bool // the blocked goroutine waits on this; true = approved
}

// PendingConfirmStore is a thread-safe store for destructive operations
// awaiting user confirmation. The daemon adds entries when it intercepts
// destructive commands; the TUI polls the snapshot and resolves them.
type PendingConfirmStore struct {
	mu  sync.Mutex
	ops map[string]*PendingConfirmation
}

// NewPendingConfirmStore creates an empty store.
func NewPendingConfirmStore() *PendingConfirmStore {
	return &PendingConfirmStore{
		ops: make(map[string]*PendingConfirmation),
	}
}

// Add stores a pending confirmation. The caller should block on pc.Decision
// after calling Add.
func (s *PendingConfirmStore) Add(pc *PendingConfirmation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ops[pc.ID] = pc
}

// Resolve sends the decision to the waiting goroutine and removes the entry.
// approverPID is the PID of the client resolving the confirmation and must
// be a positive integer. A call whose approverPID matches the original
// requester's PID is rejected — requester and approver must be distinct
// clients.
//
// Resolve is for client-initiated decisions. Internal lifecycle events
// (timeouts, encode failures) must use ResolveTimeout instead.
func (s *PendingConfirmStore) Resolve(id string, approved bool, approverPID int) error {
	if approverPID <= 0 {
		return errors.WithDetails("approverPID must be a positive integer: got %d", "id", id, "approverPID", approverPID)
	}
	s.mu.Lock()
	pc, ok := s.ops[id]
	if !ok {
		s.mu.Unlock()
		return errors.WithDetails("no pending confirmation with id %q", "id", id)
	}
	if approverPID == pc.ClientPID {
		s.mu.Unlock()
		return errors.WithDetails("requester and approver must be distinct clients (id=%q, pid=%d)", "id", id, "pid", approverPID)
	}
	delete(s.ops, id)
	s.mu.Unlock()

	// Send decision outside the lock to avoid blocking.
	pc.Decision <- approved
	return nil
}

// ResolveTimeout removes a pending confirmation without a client approver.
// It is used by internal lifecycle events (request timeouts, response-write
// failures) that need to unblock the waiting goroutine. The decision is
// always "not approved".
//
// Returns nil even when the id is unknown — the caller is typically running
// in a deferred cleanup path where the entry may have already been resolved
// by another lifecycle event, and that is not a failure.
func (s *PendingConfirmStore) ResolveTimeout(id string) {
	s.mu.Lock()
	pc, ok := s.ops[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.ops, id)
	s.mu.Unlock()

	select {
	case pc.Decision <- false:
	default:
	}
}

// Snapshot returns all pending confirmations as wire types for the TUI.
func (s *PendingConfirmStore) Snapshot() []PendingConfirm {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]PendingConfirm, 0, len(s.ops))
	for _, pc := range s.ops {
		out = append(out, PendingConfirm{
			ID:        pc.ID,
			Operation: pc.Operation,
			Tracker:   pc.Tracker,
			Key:       pc.Key,
			Prompt:    pc.Prompt,
			CreatedAt: pc.CreatedAt.UTC().Format(time.RFC3339),
			ClientPID: pc.ClientPID,
		})
	}
	return out
}

// Cleanup rejects and removes all pending confirmations older than maxAge.
func (s *PendingConfirmStore) Cleanup(maxAge time.Duration) {
	now := time.Now()
	s.mu.Lock()
	var expired []*PendingConfirmation
	for id, pc := range s.ops {
		if now.Sub(pc.CreatedAt) > maxAge {
			expired = append(expired, pc)
			delete(s.ops, id)
		}
	}
	s.mu.Unlock()

	// Reject outside the lock.
	for _, pc := range expired {
		select {
		case pc.Decision <- false:
		default:
		}
	}
}

// Len returns the number of pending confirmations.
func (s *PendingConfirmStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ops)
}
