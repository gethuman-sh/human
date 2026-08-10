package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// gatedCommenter is fakeCommenter's synchronising twin: the regression test
// reads what a goroutine posts, so the fake owns a mutex and announces the
// post rather than leaving the test to poll a shared slice.
type gatedCommenter struct {
	mu     sync.Mutex
	added  []string
	posted chan string
}

func (g *gatedCommenter) ListComments(context.Context, string) ([]tracker.Comment, error) {
	return nil, nil
}

func (g *gatedCommenter) AddComment(_ context.Context, _ string, body string) (*tracker.Comment, error) {
	g.mu.Lock()
	g.added = append(g.added, body)
	g.mu.Unlock()
	select {
	case g.posted <- body:
	default:
	}
	return &tracker.Comment{Body: body, Created: time.Now()}, nil
}

// The registry is package state; a test that leaves a run or a queue-movement
// timestamp behind changes the verdict of the next one.
func resetDeployRunsForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(resetDeployRuns)
	resetDeployRuns()
}

// The ticket's headline case: a deploy still queued behind the unbounded
// deployGate, with its [human:deploy-started] marker over an hour old, must
// not be reddened while it is perfectly healthy (SC-4150).
func TestReconcileStuckRunning_sparesAQueuedDeploy(t *testing.T) {
	resetDeployRunsForTest(t)
	deployGate.Lock() // stand in for a deploy already running
	c := &gatedCommenter{posted: make(chan string, 4)}
	p := &fakeDeployer{alreadyMerged: true}
	deps := newDeps(nil, nil, p)
	deps.Commenter = c

	done := make(chan error, 1)
	go func() {
		done <- deps.StartDeploy(context.Background(),
			StartDeployRequest{PMKey: "SC-1", Title: "t", PRBody: "b", Branch: "feat/x"})
	}()
	require.Contains(t, <-c.posted, DeployStartedHeader)
	// The marker is posted BEFORE DeployBranch registers, so wait for the
	// registration rather than racing it.
	require.Eventually(t, func() bool { _, ok := DeployRunSince("SC-1"); return ok },
		2*time.Second, time.Millisecond)

	now := time.Now()
	cards := []ReconcileCard{{
		Key:      "SC-1",
		Comments: []tracker.Comment{cmt(DeployStartedHeader+"\nbranch: feat/x", now.Add(-61*time.Minute))},
	}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), DeployRun: DeployRunSince}, now)

	assert.Equal(t, 0, n, "a healthy deploy still in the queue must never be reddened")
	assert.Empty(t, posted)
	assert.Zero(t, p.call, "the engine has not touched the forge: it is still behind the gate")

	deployGate.Unlock()
	require.NoError(t, <-done)
}

// Bounded, not exempt: a deploy the engine itself dequeued but that never
// returned is as dead as any other stage, judged by its OWN clock.
func TestReconcileStuckRunning_stillRedsADeployDeadPastItsOwnClock(t *testing.T) {
	resetDeployRunsForTest(t)
	now := time.Now()
	deployRunQueued("SC-1", now.Add(-70*time.Minute))
	deployRunDequeued("SC-1", now.Add(-61*time.Minute))

	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{cmt(DeployStartedHeader, now.Add(-61*time.Minute))}}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), DeployRun: DeployRunSince}, now)

	assert.Equal(t, 1, n, "past its OWN clock a deploy that never returned is as dead as any other stage")
	require.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, DeployFailedHeader))
}

// AD2's rule: a queued deploy is healthy exactly while the queue is
// advancing. SC-1 has waited 3 hours, but another key dequeued 5 minutes ago —
// the queue is moving, so SC-1's wait is not yet judgeable.
func TestReconcileStuckRunning_sparesADeployQueuedBehindAMovingQueue(t *testing.T) {
	resetDeployRunsForTest(t)
	now := time.Now()
	deployRunQueued("SC-1", now.Add(-3*time.Hour))
	deployRunQueued("SC-2", now.Add(-4*time.Hour))
	deployRunDequeued("SC-2", now.Add(-5*time.Minute))

	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{cmt(DeployStartedHeader, now.Add(-3*time.Hour))}}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), DeployRun: DeployRunSince}, now)

	assert.Equal(t, 0, n, "the queue is still moving; SC-1's wait is not yet judgeable")
	assert.Empty(t, posted)
}

// AD2's bound: once the queue itself has stopped moving, a queued deploy is
// judgeable like any other — the sparing is process-wide but still bounded.
func TestReconcileStuckRunning_redsADeployWhoseQueueNeverMoved(t *testing.T) {
	resetDeployRunsForTest(t)
	now := time.Now()
	deployRunQueued("SC-1", now.Add(-3*time.Hour))
	deployRunQueued("SC-2", now.Add(-4*time.Hour))
	deployRunDequeued("SC-2", now.Add(-3*time.Hour))

	cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{cmt(DeployStartedHeader, now.Add(-3*time.Hour))}}}
	var posted []struct{ Key, Body string }
	n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
		ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), DeployRun: DeployRunSince}, now)

	assert.Equal(t, 1, n, "the queue has not moved in three hours; SC-1 is judgeable")
	require.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, DeployFailedHeader))
}

// The engine's clock covers every deploy entry route, because it is keyed by
// the in-flight run rather than by which marker is newest: the approve
// branch's [human:pr-review-passed] and the deploy fixer's
// [human:deploy-fix-started] used to get the ordinary 15-minute grace with a
// 45-minute CI gate ahead of them.
func TestStuckGrace_coversEveryDeployEntryRoute(t *testing.T) {
	for _, header := range []string{DeployStartedHeader, PRReviewPassedHeader, DeployFixStartedHeader} {
		t.Run(header, func(t *testing.T) {
			resetDeployRunsForTest(t)
			now := time.Now()
			deployRunQueued("SC-1", now.Add(-25*time.Minute))
			deployRunDequeued("SC-1", now.Add(-20*time.Minute))
			cards := []ReconcileCard{{Key: "SC-1", Comments: []tracker.Comment{cmt(header, now.Add(-20*time.Minute))}}}
			var posted []struct{ Key, Body string }
			n := reconcileStuckRunning(context.Background(), takeoverSet(cards, alwaysReachable),
				ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), DeployRun: DeployRunSince}, now)
			assert.Equal(t, 0, n, "an in-flight deploy gets the deploy grace whichever marker started it")
			assert.Empty(t, posted)
		})
	}
}

// The registry itself: a run must be visible as queued while behind the gate,
// dequeued once past it, and gone entirely once DeployBranch returns.
// blockingDeployer wraps fakeDeployer to pause DeployBranch's engine call —
// BranchMerged is the first thing the engine calls once past the gate — so a
// test can probe the registry from INSIDE a running call, not just before or
// after it.
type blockingDeployer struct {
	*fakeDeployer
	entered chan struct{}
	unblock chan struct{}
}

func (b *blockingDeployer) BranchMerged(ctx context.Context, workspaceDir, branch string) bool {
	close(b.entered)
	<-b.unblock
	return b.fakeDeployer.BranchMerged(ctx, workspaceDir, branch)
}

// Pins all three properties the plan's test-case table asks for: DeployRunSince
// reports ok with a non-zero DEQUEUE instant while the run is past the gate and
// still executing, and ok == false once the call has returned. Held loosely
// ("ok while running") this would already pass with deployRunDequeued deleted —
// queuedAt alone still makes the probe answer ok. The assertion is anchored to
// the moment the gate was actually released, which only the dequeue signal at
// board_transition.go can produce; without it "since" stays pinned at queuedAt
// regardless of how long the run then waited behind the gate.
func TestDeployBranch_registersAndClearsTheRun(t *testing.T) {
	resetDeployRunsForTest(t)
	deployGate.Lock() // hold the gate so this run is genuinely queued, not instantly dequeued

	entered := make(chan struct{})
	unblock := make(chan struct{})
	p := &blockingDeployer{fakeDeployer: &fakeDeployer{alreadyMerged: true}, entered: entered, unblock: unblock}
	deps := BoardTransitionDeps{Commenter: &fakeCommenter{}, Deployer: p, WorkspaceDir: "/ws", ConfigDir: "/ws"}

	queuedAt := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- deps.DeployBranch(context.Background(), "SC-1", "t", "b", "feat/x")
	}()

	require.Eventually(t, func() bool { _, ok := DeployRunSince("SC-1"); return ok },
		2*time.Second, time.Millisecond, "the run must register as queued before the gate ever opens")

	// Keep the run queued for a measurable interval before releasing the gate,
	// so a probe pinned at queuedAt is distinguishable from one that tracks the
	// actual dequeue.
	time.Sleep(30 * time.Millisecond)
	releasedAt := time.Now()
	deployGate.Unlock()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("DeployBranch never reached the engine call past the gate")
	}

	since, ok := DeployRunSince("SC-1")
	require.True(t, ok, "the registry must still answer ok while the run is past the gate and executing")
	assert.False(t, since.Before(releasedAt),
		"since must track the moment the run actually left the gate, not the moment it joined the queue")
	assert.GreaterOrEqual(t, since.Sub(queuedAt), 25*time.Millisecond,
		"since must reflect the real time spent queued behind the gate — pinned to queuedAt is the bug SC-4150 half-reintroduces")

	close(unblock)
	require.NoError(t, <-done)

	_, ok = DeployRunSince("SC-1")
	assert.False(t, ok, "the defer clears the registry once the call returns")
}
