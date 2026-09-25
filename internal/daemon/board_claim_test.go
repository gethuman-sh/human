package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	humanerrors "github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// claimComment builds a stamped claim comment with an explicit server id and
// creation time, mirroring what a daemon posts and a backend echoes.
func claimComment(id string, stage BoardStage, daemonID string, at time.Time) tracker.Comment {
	body := marker.Sign(ClaimHeader+"\n"+ClaimStagePrefix+" "+string(stage), daemonID, "")
	return tracker.Comment{ID: id, Body: body, Created: at}
}

func TestClaimStage(t *testing.T) {
	stage, ok := claimStage("[human:claim]\nstage: implementation\ndaemon: d1")
	require.True(t, ok)
	assert.Equal(t, BoardImplementation, stage)

	// A non-claim body is not a claim.
	_, ok = claimStage("[human:planning-started]")
	assert.False(t, ok)

	// A claim with no stage line is still a claim but names no stage, so it
	// matches no target stage.
	stage, ok = claimStage("[human:claim]\ndaemon: d1")
	require.True(t, ok)
	assert.Empty(t, string(stage))
}

func TestClaimIDLess(t *testing.T) {
	// Numeric ids compare numerically: "9" precedes "10".
	assert.True(t, claimIDLess("9", "10"))
	assert.False(t, claimIDLess("10", "9"))
	// A non-numeric id falls back to a byte-wise compare.
	assert.True(t, claimIDLess("abc", "abd"))
	assert.False(t, claimIDLess("10", "10"))
}

func TestLatestStartedFor(t *testing.T) {
	comments := []tracker.Comment{
		cmt("[human:planning-started]", time.Unix(10, 0)),
		cmt("[human:planning-started]", time.Unix(30, 0)),
		cmt("[human:implementation-started]", time.Unix(20, 0)),
	}
	c, ok := latestStartedFor(comments, BoardPlanning)
	require.True(t, ok)
	assert.Equal(t, time.Unix(30, 0), c.Created)

	_, ok = latestStartedFor(comments, BoardVerification)
	assert.False(t, ok)
}

func TestCommentNewer(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(1001, 0)
	// Later Created always wins regardless of ID.
	assert.True(t, commentNewer(tracker.Comment{ID: "1", Created: t1}, tracker.Comment{ID: "9", Created: t0}))
	// Same second: higher comment ID wins.
	assert.True(t, commentNewer(tracker.Comment{ID: "1681", Created: t0}, tracker.Comment{ID: "1680", Created: t0}))
	assert.False(t, commentNewer(tracker.Comment{ID: "1680", Created: t0}, tracker.Comment{ID: "1681", Created: t0}))
	// Same second, equal/empty ids: degrades to false (first-seen wins), matching .After.
	assert.False(t, commentNewer(tracker.Comment{Created: t0}, tracker.Comment{Created: t0}))
}

// SC-1701 (comment 1704): a fresh claim that ties in the same second as an
// unrelated *-started for the stage but carries a HIGHER comment ID is genuinely
// newer than that launch, so it must stay live rather than being dropped as
// fulfilled.
func TestLiveClaims_sameSecondClaimAfterStartedStaysLive(t *testing.T) {
	tie := time.Unix(2000, 0)
	comments := []tracker.Comment{
		{ID: "10", Body: ImplementationStartedHeader, Created: tie},
		claimComment("11", BoardImplementation, "d1", tie),
	}
	ids := claimIDs(liveClaims(comments, BoardImplementation, tie))
	assert.Equal(t, []string{"11"}, ids)

	// A claim with a LOWER id than the started marker was posted before the
	// launch and is correctly fulfilled.
	older := []tracker.Comment{
		{ID: "11", Body: ImplementationStartedHeader, Created: tie},
		claimComment("10", BoardImplementation, "d1", tie),
	}
	assert.Empty(t, liveClaims(older, BoardImplementation, tie))
}

// claimIDs renders live claims as their server ids, the shape the arbitration
// assertions read.
func claimIDs(cs []tracker.Comment) []string {
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestClaimWon_singleClaimWins(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("5", BoardImplementation, "d1", now),
	}
	won, _ := claimWon(comments, BoardImplementation, "5", "d1", now)
	assert.True(t, won)
}

func TestClaimWon_lowestIDWins(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("7", BoardImplementation, "d1", now),
		claimComment("9", BoardImplementation, "d2", now),
	}
	// The lower id wins; the higher id backs off.
	won, _ := claimWon(comments, BoardImplementation, "7", "d1", now)
	assert.True(t, won)
	won, _ = claimWon(comments, BoardImplementation, "9", "d2", now)
	assert.False(t, won)
}

func TestClaimWon_numericNotLexical(t *testing.T) {
	now := time.Unix(1000, 0)
	// Lexically "10" < "9", but numerically 9 < 10 — id 9 must win.
	comments := []tracker.Comment{
		claimComment("9", BoardImplementation, "d1", now),
		claimComment("10", BoardImplementation, "d2", now),
	}
	won, _ := claimWon(comments, BoardImplementation, "9", "d1", now)
	assert.True(t, won)
	won, _ = claimWon(comments, BoardImplementation, "10", "d2", now)
	assert.False(t, won)
}

func TestClaimWon_supersededByStartedBacksOff(t *testing.T) {
	now := time.Unix(1000, 0)
	// Two claims, then a started marker landed AFTER them: the launch already
	// happened, so a claimant re-reading now must back off rather than double
	// launch.
	comments := []tracker.Comment{
		claimComment("7", BoardImplementation, "d1", now.Add(-2*time.Second)),
		claimComment("9", BoardImplementation, "d2", now.Add(-2*time.Second)),
		cmt(ImplementationStartedHeader, now.Add(-time.Second)),
	}
	won, _ := claimWon(comments, BoardImplementation, "7", "d1", now)
	assert.False(t, won)
	won, _ = claimWon(comments, BoardImplementation, "9", "d2", now)
	assert.False(t, won)
}

func TestClaimWon_expiredCompetitorIgnored(t *testing.T) {
	now := time.Unix(10000, 0)
	// A lower-id competitor whose claim never produced a started marker and is
	// older than the TTL is dead: it must not wedge the stage, so the live
	// higher-id claim wins.
	comments := []tracker.Comment{
		claimComment("3", BoardImplementation, "dead", now.Add(-2*ClaimTTL)),
		claimComment("8", BoardImplementation, "d1", now),
	}
	won, _ := claimWon(comments, BoardImplementation, "8", "d1", now)
	assert.True(t, won)
}

func TestClaimWon_retryAfterPreviousRunWins(t *testing.T) {
	now := time.Unix(10000, 0)
	// A previous run: its claim was fulfilled by a started marker, then failed.
	// A fresh retry claim posted after that started marker is live and wins.
	comments := []tracker.Comment{
		claimComment("3", BoardImplementation, "d1", now.Add(-time.Hour)),
		cmt(ImplementationStartedHeader, now.Add(-time.Hour).Add(time.Second)),
		cmt(ImplementationFailedHeader+"\nboom", now.Add(-30*time.Minute)),
		claimComment("20", BoardImplementation, "d1", now),
	}
	won, _ := claimWon(comments, BoardImplementation, "20", "d1", now)
	assert.True(t, won)
}

func TestClaimWon_recoversOwnIDWhenNotEchoed(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("4", BoardImplementation, "d1", now),
	}
	// Backend did not echo an id: the claim is recovered by this daemon's stamp.
	won, _ := claimWon(comments, BoardImplementation, "", "d1", now)
	assert.True(t, won)
}

func TestClaimWon_unidentifiableRefusesToWin(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("4", BoardImplementation, "other", now),
	}
	// No echoed id and no claim carrying our daemon id: refuse to win rather than
	// risk a double launch.
	won, _ := claimWon(comments, BoardImplementation, "", "d1", now)
	assert.False(t, won)
}

func TestClaimWon_otherStageDoesNotContend(t *testing.T) {
	now := time.Unix(1000, 0)
	// A lower-id claim for a DIFFERENT stage never blocks this stage's claim.
	comments := []tracker.Comment{
		claimComment("2", BoardPlanning, "d2", now),
		claimComment("6", BoardImplementation, "d1", now),
	}
	won, _ := claimWon(comments, BoardImplementation, "6", "d1", now)
	assert.True(t, won)
}

// SC-5094: two launches failed after posting their claim, leaving both claims
// standing (a *-failed marker does not fulfil a claim). The daemon's own fresh
// claim must not lose the race to its own leftovers — and must not have to wait
// out ClaimTTL to win it.
func TestClaimWon_ownStaleClaimsDoNotContend(t *testing.T) {
	now := time.Unix(100000, 0)
	comments := []tracker.Comment{
		claimComment("1", BoardImplementation, "d1", now.Add(-2*time.Minute)),
		cmt(ImplementationFailedHeader+"\nreason: boom", now.Add(-2*time.Minute).Add(time.Second)),
		claimComment("2", BoardImplementation, "d1", now.Add(-time.Minute)),
		cmt(ImplementationFailedHeader+"\nreason: boom", now.Add(-time.Minute).Add(time.Second)),
		claimComment("3", BoardImplementation, "d1", now),
	}
	won, winner := claimWon(comments, BoardImplementation, "3", "d1", now)
	assert.True(t, won, "a daemon must not lose the claim race to its own earlier claims")
	assert.Empty(t, winner.ID)

	// And it keeps winning while the leftovers are STILL within their TTL window:
	// the lockout must not come back as a mere delay. +2m is inside every claim's
	// TTL window (the leftovers expire at +3m/+4m, the fresh claim at +5m), so the
	// per-daemon collapse in liveClaims — not expiry — is what carries this call.
	wonLater, _ := claimWon(comments, BoardImplementation, "3", "d1", now.Add(2*time.Minute))
	assert.True(t, wonLater)
	require.Len(t, liveClaims(comments, BoardImplementation, now.Add(2*time.Minute)), 1,
		"the daemon's three own claims collapse to its single newest live one")
}

// SC-5094 (PR 575 round 1): two claims from the SAME daemon, posted concurrently
// so NEITHER fulfils nor expires the other, must still yield exactly one
// winner. An earlier version of the fix exempted a same-daemon claim from ever
// beating another same-daemon claim inside claimWon's win check; that exemption
// is symmetric, so each of the two concurrent claims exempted the OTHER and both
// reported won=true — two launchers reaching for the same Docker container name.
// Collapsing to one live claim per daemon in liveClaims (rather than exempting
// pairwise in claimWon) is what keeps this to one winner.
func TestClaimWon_concurrentSameDaemonClaimsYieldExactlyOneWinner(t *testing.T) {
	now := time.Unix(100000, 0)
	comments := []tracker.Comment{
		claimComment("5", BoardImplementation, "d1", now),
		claimComment("7", BoardImplementation, "d1", now),
	}
	won5, _ := claimWon(comments, BoardImplementation, "5", "d1", now)
	won7, _ := claimWon(comments, BoardImplementation, "7", "d1", now)
	assert.False(t, won5, "the older of two concurrent same-daemon claims must not also win")
	assert.True(t, won7, "the newer concurrent claim is this daemon's sole live one and wins")
}

// A peer's lower, live claim still beats a daemon's own claims even after they
// collapse to their newest: the collapse changes what contends WITHIN d1, not
// the arbitration between d1 and a peer.
func TestClaimWon_peerBeatsCollapsedSameDaemonClaims(t *testing.T) {
	now := time.Unix(100000, 0)
	comments := []tracker.Comment{
		claimComment("6", BoardImplementation, "d2", now),
		claimComment("5", BoardImplementation, "d1", now),
		claimComment("7", BoardImplementation, "d1", now),
	}
	won6, winner6 := claimWon(comments, BoardImplementation, "6", "d2", now)
	won7, winner7 := claimWon(comments, BoardImplementation, "7", "d1", now)
	assert.True(t, won6, "the peer's lower claim wins even against d1's collapsed claim")
	assert.Empty(t, winner6.ID)
	assert.False(t, won7, "d1's collapsed (newest) claim still loses to the lower peer claim")
	assert.Equal(t, "6", winner7.ID)
	assert.Equal(t, "d2", winner7.DaemonID)
}

// Supersession is same-daemon only: a peer's lower, live claim still wins, and
// the loser learns who won so the refusal can name it.
func TestClaimWon_peerLowerClaimStillWins(t *testing.T) {
	now := time.Unix(100000, 0)
	comments := []tracker.Comment{
		claimComment("2", BoardImplementation, "d2", now),
		claimComment("5", BoardImplementation, "d1", now),
	}
	won, winner := claimWon(comments, BoardImplementation, "5", "d1", now)
	assert.False(t, won)
	assert.Equal(t, "2", winner.ID)
	assert.Equal(t, "d2", winner.DaemonID)
}

// The winner reported is the LOWEST blocking claim, whatever order the tracker
// returned the thread in.
func TestClaimWon_winnerIsLowestBlockingClaim(t *testing.T) {
	now := time.Unix(100000, 0)
	comments := []tracker.Comment{
		claimComment("4", BoardImplementation, "d3", now),
		claimComment("2", BoardImplementation, "d2", now),
		claimComment("9", BoardImplementation, "d1", now),
	}
	won, winner := claimWon(comments, BoardImplementation, "9", "d1", now)
	assert.False(t, won)
	assert.Equal(t, "2", winner.ID)
	assert.Equal(t, "d3", ParseDaemonID(comments[0].Body)) // fixture sanity
}

// SC-5094 end to end: the campaign thread — two claims from THIS daemon, each
// followed by a failed launch — and a Build retry that must start an agent
// immediately rather than bouncing for ClaimTTL. Driven through a signing
// commenter so the claim this daemon posts carries machine: d1 exactly as in
// production.
func TestStartAgentStage_ownStaleClaimsDoNotBlockRetry(t *testing.T) {
	now := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			// Close enough to now that this retry stays under StageWaitThreshold: a
			// [human:stage-wait] marker is an unrelated, orthogonal signal and would
			// otherwise inflate c.added beyond the claim/started pair this test checks.
			cmt(PlanReadyHeader, now.Add(-3*time.Minute)),
			claimComment("1", BoardImplementation, "d1", now.Add(-2*time.Minute)),
			cmt(ImplementationFailedHeader+"\nreason: boom", now.Add(-2*time.Minute).Add(time.Second)),
			claimComment("2", BoardImplementation, "d1", now.Add(-time.Minute)),
			cmt(ImplementationFailedHeader+"\nreason: boom", now.Add(-time.Minute).Add(time.Second)),
		},
		nextID: 2, // this daemon's fresh claim gets id "3" — higher than both leftovers
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.Commenter = marker.NewSigningCommenter(c, "d1", "rev1")
	deps.DaemonID = "d1"

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardImplementation, To: BoardImplementation})
	require.NoError(t, err)

	assert.Equal(t, 1, l.calls, "the retry must launch, not lose to this daemon's own stale claims")
	require.Len(t, c.added, 2)
	assert.Contains(t, c.added[0], ClaimHeader)
	assert.Contains(t, c.added[1], ImplementationStartedHeader)
}

// A person's drag that loses the claim race is REFUSED with a reason naming the
// winner: it starts nothing here, and a drop that starts nothing must not look
// like a drop that did nothing (SC-5094).
func TestStartAgentStage_claimLoserSaysWhy(t *testing.T) {
	competitorAt := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt("[human:plan-ready]", competitorAt.Add(-time.Minute)),
			// A live, lower-id competitor claim from another daemon.
			claimComment("1", BoardImplementation, "other", competitorAt),
		},
		nextID: 100, // our posted claim gets a higher id than the competitor's "1"
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.ErrorIs(t, err, ErrClaimLost)
	assert.Contains(t, humanerrors.CauseChain(err), "other", "the refusal names the daemon that won")

	assert.Zero(t, l.calls, "loser must not launch")
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], ClaimHeader)
	for _, body := range c.added {
		assert.NotContains(t, body, ImplementationStartedHeader)
	}
}

// A machine-driven move carries a Cause and keeps the old silence: the winner is
// starting the stage and nobody is waiting on an answer.
func TestStartAgentStage_chainedLaunchLosesClaimSilently(t *testing.T) {
	competitorAt := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt("[human:plan-ready]", competitorAt.Add(-time.Minute)),
			claimComment("1", BoardImplementation, "other", competitorAt),
		},
		nextID: 100,
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation, Cause: WaitCauseChain})
	require.NoError(t, err)
	assert.Zero(t, l.calls)
}

// The automatic retry sees a refusal, not a failure, so its attempt is refunded
// (SC-2989) rather than charged for a launch another daemon made.
func TestApplyRetryTransition_lostClaimIsARefusalNotAnError(t *testing.T) {
	competitorAt := time.Now()
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt("[human:plan-ready]", competitorAt.Add(-time.Minute)),
			claimComment("1", BoardImplementation, "other", competitorAt),
		},
		nextID: 100,
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"

	launched, err := deps.ApplyRetryTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)
	assert.False(t, launched)
}

// A provisioned daemon that holds the lowest claim proceeds to post the started
// marker and launch.
func TestStartAgentStage_claimWinnerLaunches(t *testing.T) {
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt("[human:plan-ready]", time.Now().Add(-time.Minute)),
			// A live competitor claim with a HIGHER id than ours will get.
			claimComment("999", BoardImplementation, "other", time.Now()),
		},
		// our posted claim gets id "1", lower than the competitor's "999"
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err)

	assert.Equal(t, 1, l.calls, "winner must launch")
	require.Len(t, c.added, 2)
	assert.Contains(t, c.added[0], ClaimHeader)
	assert.Contains(t, c.added[1], ImplementationStartedHeader)
}

// A daemon whose host fails a launch-critical doctor check (here: an expired
// Claude session) must neither claim nor launch the stage: no [human:claim], no
// started marker, no failed marker — the handoff is left unclaimed for a healthy
// daemon and the failure surfaces only on this host (SC-912).
func TestStartAgentStage_launchGateSkipsClaimAndLaunch(t *testing.T) {
	c := &fakeCommenter{
		comments: []tracker.Comment{
			cmt("[human:plan-ready]", time.Now().Add(-time.Minute)),
		},
	}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	deps.DaemonID = "d1"
	deps.LaunchGate = func(context.Context) []DoctorCheck {
		return []DoctorCheck{{ID: "claude-auth", Name: "Claude authentication", OK: false, Detail: "session expired"}}
	}

	err := deps.ApplyTransition(context.Background(),
		BoardTransitionRequest{PMKey: "SC-1", From: BoardPlanning, To: BoardImplementation})
	require.NoError(t, err, "a launch-gated stage leaves the work silently, not an error")

	assert.Zero(t, l.calls, "gated daemon must not launch")
	assert.Empty(t, c.added, "gated daemon must post no claim, started, or failed marker")
}

// ClassifyMarker must ignore a claim marker: it is content, not a stage
// transition, so it never moves a card (must stay out of orderedMarkerSpecs).
func TestClaimMarker_notClassified(t *testing.T) {
	_, _, ok := ClassifyMarker(marker.Sign(ClaimHeader+"\nstage: implementation", "d1", ""))
	assert.False(t, ok)
}

// An un-provisioned daemon (no id) has no identity to arbitrate with, so it
// skips the claim and launches directly — single-daemon behavior, unchanged.
func TestWinClaim_noDaemonIDSkips(t *testing.T) {
	c := &fakeCommenter{}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	won, _, err := deps.winClaim(context.Background(), "SC-1", BoardImplementation)
	require.NoError(t, err)
	assert.True(t, won)
	assert.Empty(t, c.added, "no claim posted when un-provisioned")
}

// A failure posting the claim surfaces as an error, not a silent back-off.
func TestWinClaim_postErrorPropagates(t *testing.T) {
	c := &fakeCommenter{addErr: errors.New("tracker down")}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	deps.DaemonID = "d1"
	_, _, err := deps.winClaim(context.Background(), "SC-1", BoardImplementation)
	require.Error(t, err)
}

// A failure re-reading the thread after claiming surfaces as an error.
func TestWinClaim_reReadErrorPropagates(t *testing.T) {
	deps := BoardTransitionDeps{Commenter: listErrCommenter{&fakeCommenter{}}, DaemonID: "d1"}
	_, _, err := deps.winClaim(context.Background(), "SC-1", BoardImplementation)
	require.Error(t, err)
}
