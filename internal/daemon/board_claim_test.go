package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

// claimComment builds a stamped claim comment with an explicit server id and
// creation time, mirroring what a daemon posts and a backend echoes.
func claimComment(id string, stage BoardStage, daemonID string, at time.Time) tracker.Comment {
	body := StampDaemon(ClaimHeader+"\n"+ClaimStagePrefix+" "+string(stage), daemonID)
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
	at, ok := latestStartedFor(comments, BoardPlanning)
	require.True(t, ok)
	assert.Equal(t, time.Unix(30, 0), at)

	_, ok = latestStartedFor(comments, BoardVerification)
	assert.False(t, ok)
}

func TestClaimWon_singleClaimWins(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("5", BoardImplementation, "d1", now),
	}
	assert.True(t, claimWon(comments, BoardImplementation, "5", "d1", now))
}

func TestClaimWon_lowestIDWins(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("7", BoardImplementation, "d1", now),
		claimComment("9", BoardImplementation, "d2", now),
	}
	// The lower id wins; the higher id backs off.
	assert.True(t, claimWon(comments, BoardImplementation, "7", "d1", now))
	assert.False(t, claimWon(comments, BoardImplementation, "9", "d2", now))
}

func TestClaimWon_numericNotLexical(t *testing.T) {
	now := time.Unix(1000, 0)
	// Lexically "10" < "9", but numerically 9 < 10 — id 9 must win.
	comments := []tracker.Comment{
		claimComment("9", BoardImplementation, "d1", now),
		claimComment("10", BoardImplementation, "d2", now),
	}
	assert.True(t, claimWon(comments, BoardImplementation, "9", "d1", now))
	assert.False(t, claimWon(comments, BoardImplementation, "10", "d2", now))
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
	assert.False(t, claimWon(comments, BoardImplementation, "7", "d1", now))
	assert.False(t, claimWon(comments, BoardImplementation, "9", "d2", now))
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
	assert.True(t, claimWon(comments, BoardImplementation, "8", "d1", now))
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
	assert.True(t, claimWon(comments, BoardImplementation, "20", "d1", now))
}

func TestClaimWon_recoversOwnIDWhenNotEchoed(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("4", BoardImplementation, "d1", now),
	}
	// Backend did not echo an id: the claim is recovered by this daemon's stamp.
	assert.True(t, claimWon(comments, BoardImplementation, "", "d1", now))
}

func TestClaimWon_unidentifiableRefusesToWin(t *testing.T) {
	now := time.Unix(1000, 0)
	comments := []tracker.Comment{
		claimComment("4", BoardImplementation, "other", now),
	}
	// No echoed id and no claim carrying our daemon id: refuse to win rather than
	// risk a double launch.
	assert.False(t, claimWon(comments, BoardImplementation, "", "d1", now))
}

func TestClaimWon_otherStageDoesNotContend(t *testing.T) {
	now := time.Unix(1000, 0)
	// A lower-id claim for a DIFFERENT stage never blocks this stage's claim.
	comments := []tracker.Comment{
		claimComment("2", BoardPlanning, "d2", now),
		claimComment("6", BoardImplementation, "d1", now),
	}
	assert.True(t, claimWon(comments, BoardImplementation, "6", "d1", now))
}

// A provisioned daemon that loses the claim launches nothing and posts no
// started marker — it backs off silently (no error), leaving the stage to the
// lower-id claimant.
func TestStartAgentStage_claimLoserBacksOff(t *testing.T) {
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
	require.NoError(t, err)

	assert.Zero(t, l.calls, "loser must not launch")
	// Only the claim was posted; no started marker.
	require.Len(t, c.added, 1)
	assert.Contains(t, c.added[0], ClaimHeader)
	for _, body := range c.added {
		assert.NotContains(t, body, ImplementationStartedHeader)
	}
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

// ClassifyMarker must ignore a claim marker: it is content, not a stage
// transition, so it never moves a card (must stay out of orderedMarkerSpecs).
func TestClaimMarker_notClassified(t *testing.T) {
	_, _, ok := ClassifyMarker(StampDaemon(ClaimHeader+"\nstage: implementation", "d1"))
	assert.False(t, ok)
}

// An un-provisioned daemon (no id) has no identity to arbitrate with, so it
// skips the claim and launches directly — single-daemon behavior, unchanged.
func TestWinClaim_noDaemonIDSkips(t *testing.T) {
	c := &fakeCommenter{}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	won, err := deps.winClaim(context.Background(), "SC-1", BoardImplementation)
	require.NoError(t, err)
	assert.True(t, won)
	assert.Empty(t, c.added, "no claim posted when un-provisioned")
}

// A failure posting the claim surfaces as an error, not a silent back-off.
func TestWinClaim_postErrorPropagates(t *testing.T) {
	c := &fakeCommenter{addErr: errors.New("tracker down")}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	deps.DaemonID = "d1"
	_, err := deps.winClaim(context.Background(), "SC-1", BoardImplementation)
	require.Error(t, err)
}

// A failure re-reading the thread after claiming surfaces as an error.
func TestWinClaim_reReadErrorPropagates(t *testing.T) {
	deps := BoardTransitionDeps{Commenter: listErrCommenter{&fakeCommenter{}}, DaemonID: "d1"}
	_, err := deps.winClaim(context.Background(), "SC-1", BoardImplementation)
	require.Error(t, err)
}
