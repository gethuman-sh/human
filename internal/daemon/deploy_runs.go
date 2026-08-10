package daemon

import (
	"sync"
	"time"
)

// DeployRunProbe answers, for pmKey, the instant from which the deploy engine's
// own clock defensibly runs on THIS machine — and whether the machine knows
// anything at all.
//
// It exists because the deploy stage has two clocks: the ticket's marker clock
// (StageEnteredAt, set when [human:deploy-started] is posted) and the engine's
// own, which does not start until DeployBranch is past deployGate. The gate is
// deliberately unbounded (one deploy at a time, SC-296), so a deploy queued
// behind another was charged its predecessors' waits against a fixed 60-minute
// budget and reddened while perfectly healthy (SC-4150).
//
// !ok means this machine knows nothing — a peer daemon's deploy, or a run lost
// to a restart — and the caller falls back to the marker clock, so the sweep is
// never blind to a genuinely dead deploy.
type DeployRunProbe func(pmKey string) (since time.Time, ok bool)

// deployRunState is one key's in-flight DeployBranch call(s). refs, rather than a
// bare timestamp, because two calls for the same key can overlap (a board Deploy
// re-drop racing `human deploy`) and the first to finish must not erase the
// second's record.
type deployRunState struct {
	refs       int
	queuedAt   time.Time
	dequeuedAt time.Time // zero while the run is still behind the gate
}

// deployRuns is process-local state about the engine's own runs, exactly like
// deployGate beside it: the deploy is not a container the sweep can list, so
// this is the only place its progress is knowable.
var deployRuns = struct {
	sync.Mutex
	runs map[string]*deployRunState
	// lastDequeueAt is when the gate last admitted ANY run: the moment the queue
	// last moved. A run still waiting is healthy while the queue is advancing,
	// and judgeable once it has stopped — which is what keeps a queued deploy
	// bounded rather than exempt.
	lastDequeueAt time.Time
}{runs: map[string]*deployRunState{}}

func deployRunQueued(pmKey string, at time.Time) {
	deployRuns.Lock()
	defer deployRuns.Unlock()
	s := deployRuns.runs[pmKey]
	if s == nil {
		s = &deployRunState{queuedAt: at}
		deployRuns.runs[pmKey] = s
	}
	s.refs++
}

func deployRunDequeued(pmKey string, at time.Time) {
	deployRuns.Lock()
	defer deployRuns.Unlock()
	if s := deployRuns.runs[pmKey]; s != nil {
		s.dequeuedAt = at
	}
	deployRuns.lastDequeueAt = at
}

func deployRunFinished(pmKey string) {
	deployRuns.Lock()
	defer deployRuns.Unlock()
	s := deployRuns.runs[pmKey]
	if s == nil {
		return
	}
	s.refs--
	if s.refs <= 0 {
		delete(deployRuns.runs, pmKey)
		return
	}
	// A sibling run for the same key is still behind the gate: only one run can
	// be past it at a time, so what remains is queued.
	s.dequeuedAt = time.Time{}
}

// DeployRunSince is the production DeployRunProbe.
func DeployRunSince(pmKey string) (time.Time, bool) {
	deployRuns.Lock()
	defer deployRuns.Unlock()
	s := deployRuns.runs[pmKey]
	if s == nil {
		return time.Time{}, false
	}
	if !s.dequeuedAt.IsZero() {
		return s.dequeuedAt, true
	}
	since := s.queuedAt
	if deployRuns.lastDequeueAt.After(since) {
		since = deployRuns.lastDequeueAt
	}
	return since, true
}

// resetDeployRuns clears the registry. Package state outlives a test, so a test
// that registers a run must not change the next one's verdict.
func resetDeployRuns() {
	deployRuns.Lock()
	defer deployRuns.Unlock()
	deployRuns.runs = map[string]*deployRunState{}
	deployRuns.lastDequeueAt = time.Time{}
}
