package daemon

import (
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// ConfirmRetention is how long a confirmation entry (any state) is kept in
// memory. Approved and denied entries must outlive their resolution so a
// client that lost its connection can still learn the decision by ID, and
// an approved grant stays redeemable until the requester returns.
const ConfirmRetention = 24 * time.Hour

// ConfirmState is the lifecycle state of a destructive-operation permission
// request.
//
// pending → denied              (user rejected)
// pending → approved            (user granted; grant is consumed one-time
//
//	when the client re-submits the operation)
//
// The daemon never executes anything on approval — an approval is a grant,
// and acting on it is the requesting client's job. A grant nobody redeems
// simply expires with the retention sweep.
type ConfirmState string

const (
	ConfirmPending  ConfirmState = "pending"
	ConfirmApproved ConfirmState = "approved"
	ConfirmDenied   ConfirmState = "denied"
)

// PendingConfirmation is a destructive operation's permission request: what
// is to be done (operation kind) and to which ticket — deliberately nothing
// more. The payload details (e.g. an edit's content) are not captured; the
// requesting agent is trusted with those once the operation is granted.
type PendingConfirmation struct {
	ID         string
	Operation  string // "DeleteIssue", "EditIssue", ...
	Tracker    string // tracker kind, e.g. "jira"
	Key        string // issue key, e.g. "KAN-1"
	Prompt     string // human-readable, e.g. "Delete KAN-1?"
	ClientPID  int    // PID of the Claude instance that triggered the operation
	CreatedAt  time.Time
	ResolvedAt time.Time
	State      ConfirmState
}

// PendingConfirmStore is a thread-safe queue of permission requests and
// their decisions. Entries are pure state — no parked goroutines, no
// captured commands — so any client can submit, any UI (TUI, desktop app)
// can decide, and any client can query or redeem the outcome later by ID.
// The map is the in-process source of truth; an optional persistence sink
// (WithPersistence) mirrors every mutation so approvals survive a daemon
// restart. Entries are swept after ConfirmRetention in both layers.
type PendingConfirmStore struct {
	mu  sync.Mutex
	ops map[string]*PendingConfirmation

	// sink mirrors mutations to durable storage; nil means memory-only.
	// Sink failures are logged, never propagated: a broken disk degrades
	// durability, not the correctness of the running daemon.
	sink   confirmPersistence
	logger zerolog.Logger

	// failedDeletes tracks grant consumptions whose durable delete failed —
	// the one direction where an inconsistency could resurrect an already-
	// redeemed grant as approved after a restart. Cleanup's periodic tick
	// retries them until the sink recovers.
	failedDeletes map[string]struct{}
}

// NewPendingConfirmStore creates an empty, memory-only store.
func NewPendingConfirmStore() *PendingConfirmStore {
	return &PendingConfirmStore{
		ops:           make(map[string]*PendingConfirmation),
		failedDeletes: make(map[string]struct{}),
	}
}

// WithPersistence attaches a durable sink: stale rows are pruned, the
// survivors are absorbed into the map (a restarted daemon re-offers undecided
// prompts and honors unredeemed grants), and every later mutation writes
// through. Call once at startup before the store is shared; a load failure
// leaves the store memory-only and is returned for the caller to log.
func (s *PendingConfirmStore) WithPersistence(p confirmPersistence, logger zerolog.Logger) error {
	if err := p.DeleteOlderThan(time.Now().Add(-ConfirmRetention)); err != nil {
		return errors.WrapWithDetails(err, "pruning persisted confirmations")
	}
	loaded, err := p.LoadAll()
	if err != nil {
		return errors.WrapWithDetails(err, "loading persisted confirmations")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range loaded {
		pc := loaded[i]
		if _, exists := s.ops[pc.ID]; exists {
			continue
		}
		s.ops[pc.ID] = &pc
	}
	s.sink = p
	s.logger = logger
	return nil
}

// persistDelete writes a removal through to the sink; callers hold s.mu.
// consumed marks grant redemptions, whose failed deletes must be retried
// (Cleanup) because a resurrected grant could authorize a second execution.
func (s *PendingConfirmStore) persistDelete(id string, consumed bool) {
	if s.sink == nil {
		return
	}
	if err := s.sink.Delete(id); err != nil {
		if consumed {
			s.failedDeletes[id] = struct{}{}
			s.logger.Error().Err(err).Str("id", id).Msg("confirm store: persisting grant consumption failed; will retry")
			return
		}
		s.logger.Warn().Err(err).Str("id", id).Msg("confirm store: persisting removal failed")
	}
}

// Submit stores a new pending permission request. When an entry with the
// same ID already exists, the submit is idempotent: the existing entry is
// left untouched so a client retrying with its own ID cannot duplicate the
// prompt or reset an already-made decision.
func (s *PendingConfirmStore) Submit(pc *PendingConfirmation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.ops[pc.ID]; exists {
		return
	}
	pc.State = ConfirmPending
	s.ops[pc.ID] = pc
	if s.sink != nil {
		if err := s.sink.Insert(*pc); err != nil {
			// Losing this write means the prompt is lost on restart — exactly
			// the pre-persistence behavior, so a warning suffices.
			s.logger.Warn().Err(err).Str("id", pc.ID).Msg("confirm store: persisting prompt failed")
		}
	}
}

// Get returns a copy of the entry with the given ID.
func (s *PendingConfirmStore) Get(id string) (PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pc, ok := s.ops[id]
	if !ok {
		return PendingConfirmation{}, false
	}
	return *pc, true
}

// Remove deletes an entry regardless of state. Used when the submit response
// could not be delivered, so the entry would otherwise linger with no client
// aware of its ID.
func (s *PendingConfirmStore) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ops, id)
	s.persistDelete(id, false)
}

// FindPending returns the pending entry for the given operation/tracker/key,
// if any. Lets a re-submitted command reattach to its open prompt instead of
// asking the user twice for the same thing.
func (s *PendingConfirmStore) FindPending(operation, trackerKind, key string) (PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pc := range s.ops {
		if pc.State == ConfirmPending && pc.Operation == operation && pc.Tracker == trackerKind && pc.Key == key {
			return *pc, true
		}
	}
	return PendingConfirmation{}, false
}

// ConsumeApprovedFor redeems an approved grant by the operation it covers,
// regardless of which nonce created it. The user's consent is operation-
// level (the prompt says "TransitionIssue 158?", not which client asked),
// so a requester that lost its original ID — crash, restart, legacy build —
// redeems the approval instead of prompting the user a second time. Match
// and removal are one atomic step, same as Consume.
func (s *PendingConfirmStore) ConsumeApprovedFor(operation, trackerKind, key string) (PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, pc := range s.ops {
		if pc.State == ConfirmApproved && pc.Operation == operation && pc.Tracker == trackerKind && pc.Key == key {
			delete(s.ops, id)
			s.persistDelete(id, true)
			return *pc, true
		}
	}
	return PendingConfirmation{}, false
}

// FindDenied returns the denied entry for the given operation/tracker/key,
// if any. A denial is the user's decision about the operation itself, so a
// retry under a fresh nonce must see it instead of opening a new prompt.
func (s *PendingConfirmStore) FindDenied(operation, trackerKind, key string) (PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pc := range s.ops {
		if pc.State == ConfirmDenied && pc.Operation == operation && pc.Tracker == trackerKind && pc.Key == key {
			return *pc, true
		}
	}
	return PendingConfirmation{}, false
}

// Consume redeems an approved grant by its unique ID: the entry is removed
// and returned, but only if it is approved AND covers the given operation/
// tracker/key — the grant authorizes exactly what the user saw in the
// prompt. Match and removal are one atomic step so a grant can never
// authorize two executions.
func (s *PendingConfirmStore) Consume(id, operation, trackerKind, key string) (PendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pc, ok := s.ops[id]
	if !ok || pc.State != ConfirmApproved {
		return PendingConfirmation{}, false
	}
	if pc.Operation != operation || pc.Tracker != trackerKind || pc.Key != key {
		return PendingConfirmation{}, false
	}
	delete(s.ops, id)
	s.persistDelete(id, true)
	return *pc, true
}

// Resolve applies a user decision to a pending entry: approval turns it into
// a redeemable grant, denial closes it. Returns a copy of the entry so the
// caller can act on the decision (e.g. audit a denial).
//
// The approverPID != requester PID check below is only a best-effort sanity
// guard, NOT an authorization boundary: ClientPID is supplied by the client
// and the requester's PID is typically resolved inside the agent's container
// namespace while the approver's is on the host, so the two are not comparable
// as a trust signal. Actual authorization is the daemon token required to reach
// this endpoint at all — do not rely on the PID check for security.
func (s *PendingConfirmStore) Resolve(id string, approved bool, approverPID int) (PendingConfirmation, error) {
	if approverPID <= 0 {
		return PendingConfirmation{}, errors.WithDetails("approverPID must be a positive integer: got %d", "id", id, "approverPID", approverPID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pc, ok := s.ops[id]
	if !ok {
		return PendingConfirmation{}, errors.WithDetails("no pending confirmation with id %q", "id", id)
	}
	if pc.State != ConfirmPending {
		return PendingConfirmation{}, errors.WithDetails("confirmation already resolved", "id", id, "state", string(pc.State))
	}
	// Best-effort sanity guard only (see doc above): reject the degenerate
	// case where the approver reports the same PID as the requester.
	if approverPID == pc.ClientPID {
		return PendingConfirmation{}, errors.WithDetails("approver PID matches requester PID (id=%q, pid=%d)", "id", id, "pid", approverPID)
	}

	if approved {
		pc.State = ConfirmApproved
	} else {
		pc.State = ConfirmDenied
	}
	pc.ResolvedAt = time.Now()
	if s.sink != nil {
		if err := s.sink.UpdateResolved(*pc); err != nil {
			// A lost decision reverts to pending on restart and re-prompts the
			// user — annoying but safe, so a warning suffices.
			s.logger.Warn().Err(err).Str("id", id).Msg("confirm store: persisting decision failed")
		}
	}
	return *pc, nil
}

// Snapshot returns the currently pending (undecided) permission requests as
// wire types for confirmation UIs. Resolved entries are excluded — they are
// only reachable by ID via Get/confirm-status.
func (s *PendingConfirmStore) Snapshot() []PendingConfirm {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]PendingConfirm, 0, len(s.ops))
	for _, pc := range s.ops {
		if pc.State != ConfirmPending {
			continue
		}
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

// Cleanup removes entries older than maxAge, regardless of state. Pending
// requests and unredeemed grants past the age are dropped too — a client
// polling such an ID sees state "unknown" and treats it as expired.
func (s *PendingConfirmStore) Cleanup(maxAge time.Duration) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, pc := range s.ops {
		if now.Sub(pc.CreatedAt) > maxAge {
			delete(s.ops, id)
		}
	}
	if s.sink == nil {
		return
	}
	// Retry grant-consumption deletes that failed at redemption time — until
	// they land, a restart could resurrect an already-executed grant.
	for id := range s.failedDeletes {
		if err := s.sink.Delete(id); err != nil {
			s.logger.Error().Err(err).Str("id", id).Msg("confirm store: grant consumption still not persisted")
			continue
		}
		delete(s.failedDeletes, id)
	}
	if err := s.sink.DeleteOlderThan(now.Add(-maxAge)); err != nil {
		// The next tick retries; rows only outlive memory briefly.
		s.logger.Warn().Err(err).Msg("confirm store: pruning persisted confirmations failed")
	}
}

// Len returns the number of stored entries (all states).
func (s *PendingConfirmStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ops)
}
