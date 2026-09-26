package daemon

// SC-5840: a host this daemon's own proxy refused by policy is closed with no
// TLS alert, so from inside the container it is byte-identical to a dead
// network and the agent honestly records exit: outage. Before this, the
// uncharged outage re-drive repeated the same missing config line every
// reconcile tick for up to OutageWaitBound (six fixer containers in twelve
// minutes in the reported incident). These tests pin the correlation that
// reclassifies such an ending into a one-time red naming the host and the
// config line, with no relaunch, while a genuine outage keeps today's
// uncharged wait.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

func TestAdvanceDeployFix_OutageWhileTheProxyBlockedTheHost_RedsOnceNamingTheConfigLine(t *testing.T) {
	c := &fakeCommenter{comments: deployFixRunningThread(time.Unix(5, 0))}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	deps.EgressBlocked = func(string) (EgressBlock, bool) {
		return EgressBlock{Host: "github.com", At: time.Unix(5, 30), ConfigFile: "/proj/.humanconfig.yaml"}, true
	}
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"})
	require.NoError(t, err)

	require.Len(t, c.added, 1, "exactly one comment posted")
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, DeployFailedHeader), "posted body must start with %q, got %q", DeployFailedHeader, posted)
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployOutageHeader), "a blocked host must not park the card waiting: %q", b)
	}
	assert.Contains(t, posted, "github.com")
	assert.Contains(t, posted, "proxy.domains")
	assert.Contains(t, posted, "/proj/.humanconfig.yaml")
	assert.Contains(t, posted, "kind: unavailable-dependency")

	thread := append(append([]tracker.Comment{}, c.comments...), cmt(posted, time.Unix(6, 0)))
	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardFailed, card.State)
}

func TestAdvanceDeployFix_OutageWithNoBlockKeepsTheOutageMarker(t *testing.T) {
	c := &fakeCommenter{comments: deployFixRunningThread(time.Unix(5, 0))}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	deps.EgressBlocked = func(string) (EgressBlock, bool) { return EgressBlock{}, false }
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable: git fetch origin — could not resolve host"})
	require.NoError(t, err)

	require.Len(t, c.added, 1)
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, DeployOutageHeader))

	thread := append(append([]tracker.Comment{}, c.comments...), cmt(posted, time.Unix(6, 0)))
	card := DeriveBoardCard(thread, tracker.CategoryUnstarted, false)
	assert.Equal(t, BoardDoneStage, card.Stage)
	assert.Equal(t, BoardOutage, card.State)
	assert.True(t, isDeployRetry(BoardDoneStage, card))
}

func TestAdvanceDeployFix_BlockedOntoAnAlreadyFailedDoneStagePostsNothing(t *testing.T) {
	thread := append(deployFixRunningThread(time.Unix(5, 0)), cmt(DeployFailedHeader+"\nsomething else went wrong", time.Unix(9, 0)))
	c := &fakeCommenter{comments: thread}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	deps.EgressBlocked = func(string) (EgressBlock, bool) {
		return EgressBlock{Host: "github.com", At: time.Unix(5, 30), ConfigFile: "/proj/.humanconfig.yaml"}, true
	}
	err := deps.AdvanceDeployFix(context.Background(), "SC-1", DeployFixReport{Exit: ExitOutage, Summary: "the git remote was unreachable"})
	require.NoError(t, err)
	assert.Empty(t, c.added, "a red another actor owns must not be flipped")
}

func TestHandleOutageExit_BlockedHostRedsTheStageInsteadOfPausingIt(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}}
	deps := FailureDeps{
		Retry: StageRetry{
			Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
			Attempts: func(string, BoardStage) (int, error) { return 0, nil },
			Relaunch: func(string, BoardStage) (bool, error) {
				t.Fatal("must not relaunch a blocked outage")
				return false, nil
			},
			EgressBlocked: func(string) (EgressBlock, bool) {
				return EgressBlock{Host: "github.com", At: time.Unix(1, 30), ConfigFile: "/proj/.humanconfig.yaml"}, true
			},
		},
	}
	exit := RunExit{PMKey: "SC-1", Stage: BoardImplementation, AgentName: "a", Comments: c.comments}
	handled := handleOutageExit(context.Background(), exit, c, deps, endingUnknown, "")
	require.True(t, handled)
	require.Len(t, c.added, 1)
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, ImplementationFailedHeader))
	assert.Contains(t, posted, "github.com")
	assert.Contains(t, posted, "proxy.domains")
}

func TestHandleOutageExit_ModelBoundaryPauseIsNotReclassified(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}}
	deps := FailureDeps{
		Retry: StageRetry{
			Outcome:       func(string, BoardStage) (StageExit, bool) { return "", false },
			EgressBlocked: func(string) (EgressBlock, bool) { return EgressBlock{Host: "github.com", At: time.Unix(1, 30)}, true },
		},
	}
	exit := RunExit{PMKey: "SC-1", Stage: BoardImplementation, AgentName: "a", Comments: c.comments}
	handled := handleOutageExit(context.Background(), exit, c, deps, endingPaused, "model usage limit")
	require.True(t, handled)
	require.Len(t, c.added, 1)
	posted := c.added[0]
	assert.True(t, strings.HasPrefix(posted, ImplementationOutageHeader))
	assert.Contains(t, posted, "model usage limit")
}

func TestHandleOutageExit_BlockedHostWithNoProbeKeepsTodaysBehaviour(t *testing.T) {
	c := &fakeCommenter{comments: []tracker.Comment{cmt(ImplementationStartedHeader, time.Unix(1, 0))}}
	deps := FailureDeps{
		Retry: StageRetry{
			Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
			Attempts: func(string, BoardStage) (int, error) { return 0, nil },
			Relaunch: func(string, BoardStage) (bool, error) { return true, nil },
			// EgressBlocked left nil.
		},
	}
	exit := RunExit{PMKey: "SC-1", Stage: BoardImplementation, AgentName: "a", Comments: c.comments}
	handled := handleOutageExit(context.Background(), exit, c, deps, endingUnknown, "")
	require.True(t, handled)
	require.Len(t, c.added, 1)
	assert.True(t, strings.HasPrefix(c.added[0], ImplementationOutageHeader))
}

func TestClassifyRelaunch_BlockedOutageIsNobodysRelaunch(t *testing.T) {
	require.Equal(t, relaunchNone, classifyRelaunch(relaunchFacts{Outcome: ExitOutage, Recorded: true, EgressBlocked: true}))
	require.Equal(t, relaunchOutage, classifyRelaunch(relaunchFacts{Outcome: ExitOutage, Recorded: true, EgressBlocked: false}))
}

func TestReconcileFailedStages_DoesNotRedriveABlockedOutage(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, 10*time.Minute)}
	attempts := 0
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Relaunch: func(string, BoardStage) (bool, error) { return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) {
			return EgressBlock{Host: "github.com", At: now.Add(-time.Minute)}, true
		},
	}

	n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 0, n)
	assert.Zero(t, attempts, "must never even consult the attempt count")
}

func TestReconcileFailedStages_StillRedrivesARealOutage(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	cards := []ReconcileCard{failedCard("SC-1", BoardImplementation, now, 10*time.Minute)}
	attempts := 0
	retry := StageRetry{
		Max:           2,
		Outcome:       func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts:      func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Relaunch:      func(string, BoardStage) (bool, error) { return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) { return EgressBlock{}, false },
	}

	n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 1, n)
}

// TestReconcileFailedStages_NeverRedrivesAnEgressBlockedRedPastTheWindow pins
// the SC-5840 round-1 finding: the egress-blocked red carries no durable
// memory of why it is red beyond the marker body itself, so once the
// 15-minute recency window (FailedRecoveryGrace) lapses recoverableFailure
// must still refuse it — otherwise the uncharged re-drive this reclassification
// exists to stop resumes on its own, just paced by failedRecoveryBackoff
// instead of the reconcile tick.
func TestReconcileFailedStages_NeverRedrivesAnEgressBlockedRedPastTheWindow(t *testing.T) {
	resetRecoveryBackoff(t)
	now := time.Unix(100_000, 0)
	m, order := egressBlockedMarker(BoardImplementation, EgressBlock{Host: "github.com", At: now.Add(-time.Hour), ConfigFile: "/proj/.humanconfig.yaml"})
	body := markerBody(m, order...)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-2*time.Hour)),
			cmt(body, now.Add(-10*time.Minute)),
		},
	}}
	attempts := 0
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Relaunch: func(string, BoardStage) (bool, error) {
			t.Fatal("must never relaunch an egress-blocked red")
			return false, nil
		},
		// Nothing is running to re-emit the block, matching the reported
		// incident: the container that hit it is long gone.
		EgressBlocked: func(string) (EgressBlock, bool) { return EgressBlock{}, false },
	}

	n := reconcileFailedStages(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 0, n)
	assert.Zero(t, attempts, "must never even consult the attempt count")
}

func TestReconcileOutage_ConvertsABlockedSubstrateDownCardToARed(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(ImplementationOutageHeader+"\nop timed out", now.Add(-time.Minute)),
		},
	}}
	var relaunched []BoardStage
	var posted []struct{ Key, Body string }
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) {
			return EgressBlock{Host: "github.com", At: now.Add(-time.Minute), ConfigFile: "/proj/.humanconfig.yaml"}, true
		},
	}

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 0, redriven)
	assert.Equal(t, 1, handedOver)
	require.Len(t, posted, 1)
	assert.True(t, strings.HasPrefix(posted[0].Body, ImplementationFailedHeader))
	assert.Contains(t, posted[0].Body, "github.com")
	assert.Contains(t, posted[0].Body, "/proj/.humanconfig.yaml")
	assert.Empty(t, relaunched, "must never relaunch a blocked host")
}

// TestReconcileOutage_StatedResumeIsNeverOverriddenByACoincidentBlock pins the
// SC-5840 round-1 finding: a card whose newest outage marker states a
// machine-readable resume — classifyUnavailability's own model-boundary-pause
// diagnosis — must not be reclassified into a policy-blocked red just because
// this daemon's proxy also recorded refusing some host inside the window. The
// stated wait is honoured until it elapses, exactly as it was before the
// egress guard existed.
func TestReconcileOutage_StatedResumeIsNeverOverriddenByACoincidentBlock(t *testing.T) {
	now := time.Unix(10_000, 0)
	resume := now.Add(30 * time.Minute)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(ImplementationOutageHeader+"\nmodel usage limit reached\nresume: "+resume.UTC().Format(time.RFC3339), now.Add(-time.Minute)),
		},
	}}
	var relaunched []BoardStage
	var posted []struct{ Key, Body string }
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) {
			return EgressBlock{Host: "github.com", At: now.Add(-time.Minute), ConfigFile: "/proj/.humanconfig.yaml"}, true
		},
	}

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 0, redriven, "still inside the stated wait")
	assert.Equal(t, 0, handedOver, "a coincident proxy block must not reclassify a stated pause")
	assert.Empty(t, posted)
	assert.Empty(t, relaunched)
}

func TestReconcileOutage_RealOutageStillRelaunchesUncharged(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(ImplementationOutageHeader+"\nop timed out", now.Add(-time.Minute)),
		},
	}}
	var relaunched []BoardStage
	attempts := 0
	var posted []struct{ Key, Body string }
	retry := StageRetry{
		Max:           2,
		Outcome:       func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts:      func(string, BoardStage) (int, error) { attempts++; return attempts, nil },
		Relaunch:      func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) { return EgressBlock{}, false },
	}

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 1, redriven)
	assert.Equal(t, 0, handedOver)
	assert.Zero(t, attempts)
	assert.Empty(t, posted)
	assert.Equal(t, []BoardStage{BoardImplementation}, relaunched)
}

// TestReconcileOutage_BlockedWithNoPosterKeepsTheWait pins the pass's stated
// rule for an unwired PostFailed: it disables the handover only, leaving the
// indefinite uncharged wait rather than stranding the card with neither a
// statement about the blocked host nor a re-drive (SC-5840).
func TestReconcileOutage_BlockedWithNoPosterKeepsTheWait(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-time.Hour)),
			cmt(ImplementationOutageHeader+"\nop timed out", now.Add(-time.Minute)),
		},
	}}
	var relaunched []BoardStage
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(_ string, s BoardStage) (bool, error) { relaunched = append(relaunched, s); return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) {
			return EgressBlock{Host: "github.com", At: now.Add(-time.Minute)}, true
		},
	}

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 1, redriven, "with no poster the card keeps its uncharged wait")
	assert.Equal(t, 0, handedOver)
	assert.Equal(t, []BoardStage{BoardImplementation}, relaunched)
}

func TestReconcileOutage_PastTheWaitBoundStillTakesTheHandover(t *testing.T) {
	now := time.Unix(10_000, 0)
	cards := []ReconcileCard{{
		Key: "SC-1",
		Comments: []tracker.Comment{
			cmt(ImplementationStartedHeader, now.Add(-8*time.Hour)),
			cmt(ImplementationOutageHeader+"\nop timed out", now.Add(-7*time.Hour)),
		},
	}}
	var posted []struct{ Key, Body string }
	retry := StageRetry{
		Max:      2,
		Outcome:  func(string, BoardStage) (StageExit, bool) { return ExitOutage, true },
		Attempts: func(string, BoardStage) (int, error) { return 0, nil },
		Relaunch: func(string, BoardStage) (bool, error) { return true, nil },
		EgressBlocked: func(string) (EgressBlock, bool) {
			return EgressBlock{Host: "github.com", At: now.Add(-time.Minute)}, true
		},
	}

	redriven, handedOver := reconcileOutage(context.Background(), takeoverSet(cards, alwaysReachable), ReconcileDeps{LiveAgents: liveAgents(), PostFailed: capturingPoster(&posted), Retry: retry, DaemonID: "d1"}, now)

	assert.Equal(t, 0, redriven)
	assert.Equal(t, 1, handedOver)
	require.Len(t, posted, 1)
	assert.Contains(t, posted[0].Body, "waited 7h0m0s for the substrate", "the wait-bound handover fires first, not the blocked red")
}

func TestNewEgressBlockProbe_FindsARecentBlock(t *testing.T) {
	now := time.Unix(10_000, 0)
	store := NewNetworkEventStoreWithClock(func() time.Time { return now.Add(-time.Minute) })
	store.Emit("proxy", "block", "github.com")
	probe := NewEgressBlockProbe(store, func(string) string { return "/p/.humanconfig.yaml" }, func() time.Time { return now })

	blk, ok := probe("SC-1")
	require.True(t, ok)
	assert.Equal(t, "github.com", blk.Host)
	assert.Equal(t, "/p/.humanconfig.yaml", blk.ConfigFile)
}

func TestNewEgressBlockProbe_IgnoresABlockPastTheWindow(t *testing.T) {
	now := time.Unix(10_000, 0)
	store := NewNetworkEventStoreWithClock(func() time.Time { return now.Add(-16 * time.Minute) })
	store.Emit("proxy", "block", "github.com")
	probe := NewEgressBlockProbe(store, nil, func() time.Time { return now })

	_, ok := probe("SC-1")
	assert.False(t, ok)
}

func TestNewEgressBlockProbe_IgnoresNonBlockStatuses(t *testing.T) {
	now := time.Unix(10_000, 0)
	store := NewNetworkEventStoreWithClock(func() time.Time { return now })
	store.Emit("proxy", "forward", "github.com")
	store.Emit("fail", "dial-fail", "github.com")
	store.Emit("oauth", "callback", "oauth:1/x")
	probe := NewEgressBlockProbe(store, nil, func() time.Time { return now })

	_, ok := probe("SC-1")
	assert.False(t, ok)
}

func TestNewEgressBlockProbe_PrefersTheNewestBlock(t *testing.T) {
	now := time.Unix(10_000, 0)
	clock := now.Add(-2 * time.Minute)
	store := NewNetworkEventStoreWithClock(func() time.Time { return clock })
	store.Emit("proxy", "block", "a.example")
	clock = now.Add(-time.Minute)
	store.Emit("proxy", "block", "b.example")
	probe := NewEgressBlockProbe(store, nil, func() time.Time { return now })

	blk, ok := probe("SC-1")
	require.True(t, ok)
	assert.Equal(t, "b.example", blk.Host)
}

func TestNewEgressBlockProbe_DiscardsAnImplausibleHost(t *testing.T) {
	now := time.Unix(10_000, 0)
	store := NewNetworkEventStoreWithClock(func() time.Time { return now })
	store.Emit("proxy", "block", "x\nresume: 2099-01-01T00:00:00Z")
	probe := NewEgressBlockProbe(store, nil, func() time.Time { return now })

	_, ok := probe("SC-1")
	assert.False(t, ok, "an implausible host must not reach the probe's answer")

	c := &fakeCommenter{comments: []tracker.Comment{cmt(ImplementationStartedHeader, now.Add(-time.Minute))}}
	handled := postEgressBlockedFailure(context.Background(), "SC-1", BoardImplementation, c.comments,
		EgressBlock{Host: "x\nresume: 2099-01-01T00:00:00Z", At: now}, commenterPoster(c), zerolog.Nop())
	assert.False(t, handled)
	assert.Empty(t, c.added, "no body may carry a forged resume: line")
}

func TestNewEgressBlockProbe_NilStoreAndEmptyStore(t *testing.T) {
	now := time.Unix(10_000, 0)
	probe := NewEgressBlockProbe(nil, nil, func() time.Time { return now })
	require.Nil(t, probe, "a nil store disables the correlation entirely")

	store := NewNetworkEventStoreWithClock(func() time.Time { return now })
	probe2 := NewEgressBlockProbe(store, nil, func() time.Time { return now })
	_, ok := probe2("SC-1")
	assert.False(t, ok)
}

func TestEgressBlockedMarker_CarriesTheBlockerContract(t *testing.T) {
	blk := EgressBlock{Host: "github.com", At: time.Unix(1000, 0), ConfigFile: "/p/.humanconfig.yaml"}
	m, order := egressBlockedMarker(BoardImplementation, blk)
	require.NoError(t, marker.Validate(m))
	assert.Equal(t, "unavailable-dependency", m.Fields["kind"])
	assert.Contains(t, m.Fields["release"], "- \"github.com\"")
	assert.Contains(t, m.Fields["release"], "proxy.domains")
	assert.Contains(t, m.Fields["reason"], "github.com")
	body := markerBody(m, order...)
	assert.Contains(t, body, "github.com")
}
