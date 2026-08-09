package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// RunRecord is what the daemon knows about a run because IT started it: which
// ticket and stage the run belongs to. The board's exit handling reads the
// ticket from here rather than parsing it out of the hook event's agent name.
type RunRecord struct {
	AgentName string
	PMKey     string
	Stage     BoardStage
}

// RunRegistry is the daemon's record of the runs it launched, keyed by the run
// id it minted for each one and injected into the container.
//
// It exists because the hook path — the pipeline's PRIMARY driver — reconstructed
// the identity of its own work by parsing a string that came back from outside.
// The agent name is filled from HUMAN_AGENT_NAME inside the container and
// forwarded over a route authenticated only by the shared daemon token, which
// every agent container holds; the daemon then derived the ticket key from it and
// posted markers, relaunched stages and drove the review loop on that basis
// (SC-4082). A run id the registry does not hold is, by construction, not a run
// this daemon started.
//
// Claim is also the exactly-once gate. One run can raise several events that all
// look like its exit — the hook fires StopFailure on an API error and Stop when
// the turn ends, and a Stop may follow a StopFailure by contract — which drove
// the loop twice and posted the same escalation twice, sixteen seconds apart, on
// SC-3613. Per RUN rather than per name on purpose: board stage agents reuse one
// deterministic name for every rebuild, and a name-keyed lifetime dedupe is
// exactly what silently dropped every re-run's exit in SC-201.
//
// In memory, deliberately. It is the same lifetime as HookEventStore, whose
// events are the only thing that consults it: a daemon restart loses both
// together, and the durable reconcile pass is already the net for precisely that
// case ("if the daemon restarts or the hook is lost, that trigger is gone"). A
// persisted registry would add a second, differently-shaped recovery story for a
// failure the board already recovers from.
type RunRegistry struct {
	mu   sync.Mutex
	runs map[string]RunRecord
}

// NewRunRegistry returns an empty registry ready for use.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{runs: make(map[string]RunRecord)}
}

// Register mints a run id for a launch about to happen and records what it is
// for. The caller injects the returned id into the container.
//
// A nil registry returns an empty id, which leaves the run unregistered and the
// caller on the pre-registry path — the package's "nil disables" convention, so
// a partially wired daemon still launches agents.
func (r *RunRegistry) Register(agentName, pmKey string, stage BoardStage) string {
	if r == nil {
		return ""
	}
	id := newRunID()
	if id == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[id] = RunRecord{AgentName: agentName, PMKey: pmKey, Stage: stage}
	return id
}

// Claim consumes a run id: it reports what the run was for, and removes it so a
// second event carrying the same id claims nothing. The first exit event for a
// run wins; every later one is a no-op.
//
// A run id this daemon did not mint — or one already claimed — returns false,
// which the caller must treat as "not mine to act on".
func (r *RunRegistry) Claim(runID string) (RunRecord, bool) {
	if r == nil || runID == "" {
		return RunRecord{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.runs[runID]
	if !ok {
		return RunRecord{}, false
	}
	delete(r.runs, runID)
	return rec, true
}

// Forget drops a run id without claiming it, for a launch that failed after the
// id was minted. Leaving it would pin a record no event will ever arrive for.
func (r *RunRegistry) Forget(runID string) {
	if r == nil || runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.runs, runID)
}

// Len reports how many launched runs are still unclaimed. For tests and for a
// future diagnostic; the registry is otherwise write-then-consume.
func (r *RunRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

// newRunID returns an unguessable token. Unguessable matters: the id travels
// into a container and comes back over a route every container can reach, so a
// predictable id would let one run name another's. An exhausted entropy source
// returns "", which leaves the run unregistered rather than registering one a
// caller could guess.
func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
