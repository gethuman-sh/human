package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/tracker"
)

// planThread is the stranded shape: the stage started, the planner attached its
// plan, and no [human:plan-ready] followed.
func planThread(started, plan time.Time) []tracker.Comment {
	return []tracker.Comment{
		cmt(PlanningStartedHeader, started),
		cmt(PlanCommentHeader+"\n\n# Implementation Plan: SC-1 — a thing\n", plan),
	}
}

// A planning run that attached its plan and died before posting the handoff is
// judged planned, not failed: the deliverable the next stage needs is already on
// the ticket, so the daemon completes the handoff the dead run could not.
func TestHandleBoardAgentExit_PlanAttachedWithoutHandoffCompletesInstead(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{comments: planThread(time.Unix(1, 0), time.Unix(2, 0))}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitDone, true, &relaunched, &resets)
	var handedOff []string

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-planning", EventName: hookevents.EventSessionEnd},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: policy,
			OnHandoff: func(n string) { handedOff = append(handedOff, n) },
			DaemonID:  "d1", Logger: zerolog.Nop()})

	for _, body := range c.added {
		require.NotContains(t, body, PlanningFailedHeader, "a run that attached its plan must not be reddened")
	}
	require.Empty(t, relaunched, "re-planning an attached plan is the waste this closes")
	require.Equal(t, []BoardStage{BoardPlanning}, resets)
	require.Equal(t, []string{"board-SC-1-planning"}, handedOff)

	var handoffs int
	for _, body := range c.added {
		if strings.HasPrefix(strings.TrimSpace(body), PlanReadyHeader) {
			handoffs++
			require.Contains(t, body, planHandoffCompletedSentinel,
				"the daemon's own completion must say so on the ticket")
		}
	}
	require.Equal(t, 1, handoffs, "the daemon posts the handoff the dead run could not")
}

// Criterion 2 says "dies", not "exits cleanly": a crash-synthesized exit with a
// fresh plan on the ticket is judged the same way.
func TestHandleBoardAgentExit_PlanAttachedThenCrashedIsStillPlanned(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{comments: planThread(time.Unix(1, 0), time.Unix(2, 0))}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitRetryable, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-planning", EventName: hookevents.EventStopFailure},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: policy,
			DaemonID: "d1", Logger: zerolog.Nop()})

	for _, body := range c.added {
		require.NotContains(t, body, PlanningFailedHeader, "a run that attached its plan must not be reddened")
	}
	require.Empty(t, relaunched, "re-planning an attached plan is the waste this closes")

	var handoffs int
	for _, body := range c.added {
		if strings.HasPrefix(strings.TrimSpace(body), PlanReadyHeader) {
			handoffs++
			require.Contains(t, body, planHandoffCompletedSentinel)
		}
	}
	require.Equal(t, 1, handoffs, "the daemon posts the handoff the dead run could not")
}

// The zombie sweep can silence-reap a LIVE planning agent (claude still up,
// no sign of life past its idle budget) and synthesize a StopFailure carrying
// the reap observation as its ErrorType — reaching this same path before
// handleSilenceReapExit ever runs. The plan is already attached, so the
// handoff is still completed rather than the card reddened, but the body must
// say the run was stopped rather than claim it exited, and the SC-2447/
// SC-3074 reap trail must still land on the thread (SC-5090).
func TestHandleBoardAgentExit_PlanAttachedSilenceReapedStillRecordsTheReap(t *testing.T) {
	withInstantBoardExitRecheck(t)
	c := &syncCommenter{comments: planThread(time.Unix(1, 0), time.Unix(2, 0))}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitRetryable, true, &relaunched, &resets)
	reap := SilenceReap{Idle: 5 * time.Minute, Budget: 3 * time.Minute, Outstanding: "none"}

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-planning", EventName: hookevents.EventStopFailure, ErrorType: reap.ErrorType()},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: policy,
			DaemonID: "d1", Logger: zerolog.Nop()})

	for _, body := range c.added {
		require.NotContains(t, body, PlanningFailedHeader, "a run whose plan is attached must not be reddened")
	}
	require.Empty(t, relaunched, "re-planning an attached plan is the waste this closes")

	var handoffs int
	for _, body := range c.added {
		if strings.HasPrefix(strings.TrimSpace(body), PlanReadyHeader) {
			handoffs++
			require.Contains(t, body, planHandoffCompletedSentinel)
			require.NotContains(t, body, "exited before posting this handoff",
				"the run did not exit — it was silence-reaped — so the body must not claim otherwise")
			require.Contains(t, body, "idle:",
				"the silence-reap observation must still land on the thread, not be lost by the completion")
		}
	}
	require.Equal(t, 1, handoffs, "the daemon posts the handoff the reaped run could not")
}

// A re-plan whose new run died before attaching anything must still fail: the
// plan on the thread belongs to the PREVIOUS run.
func TestHandleBoardAgentExit_PlanOlderThanTheStartStillFails(t *testing.T) {
	withInstantBoardExitRecheck(t)
	comments := []tracker.Comment{
		cmt(PlanCommentHeader+"\n\n# Implementation Plan: SC-1 — a thing\n", time.Unix(2, 0)),
		cmt(PlanningStartedHeader, time.Unix(3, 0)),
	}
	c := &syncCommenter{comments: comments}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitRetryable, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-planning", EventName: hookevents.EventSessionEnd},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: policy,
			DaemonID: "d1", Logger: zerolog.Nop()})

	var sawFailed bool
	for _, body := range c.added {
		if strings.HasPrefix(strings.TrimSpace(body), PlanningFailedHeader) {
			sawFailed = true
		}
		require.False(t, strings.HasPrefix(strings.TrimSpace(body), PlanReadyHeader),
			"the plan belongs to a previous run and must not be completed")
	}
	require.True(t, sawFailed, "a run that died before attaching anything is still a failure")
	require.Equal(t, []BoardStage{BoardPlanning}, relaunched)
}

// A planner that attached its plan and then posted its OWN [human:planning-
// failed] — the exit-contract's needs-human-work instruction — must not have
// that decision silently overwritten by a handoff synthesized from the plan
// comment. The stage's own newest marker being its *-failed header is a
// decision about the ticket, not an incomplete stage (SC-5090).
func TestHandleBoardAgentExit_SelfPostedPlanningFailedIsNotOverwritten(t *testing.T) {
	withInstantBoardExitRecheck(t)
	comments := []tracker.Comment{
		cmt(PlanningStartedHeader, time.Unix(1, 0)),
		cmt(PlanCommentHeader+"\n\n# Implementation Plan: SC-1 — a thing\n", time.Unix(2, 0)),
		cmt(PlanningFailedHeader+"\nkind: missing-permission\nevidence: e\nattempted: a\nrelease: r", time.Unix(3, 0)),
	}
	c := &syncCommenter{comments: comments}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitNeedsHumanWork, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-planning", EventName: hookevents.EventSessionEnd},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: policy,
			DaemonID: "d1", Logger: zerolog.Nop()})

	for _, body := range c.added {
		require.False(t, strings.HasPrefix(strings.TrimSpace(body), PlanReadyHeader),
			"a self-posted planning-failed is a decision about the ticket and must not be overwritten by a synthesized handoff")
	}
	require.Empty(t, relaunched, "a needs-human-work exit is not automatically relaunched")
	require.Empty(t, resets, "the failed stage's retry budget is not reset by a marker this run did not post")
}

// The stage's own handoff is untouched: a run that posted plan-ready is clean
// through the existing path and draws no second, daemon-authored marker.
func TestHandleBoardAgentExit_OwnHandoffIsNotDuplicated(t *testing.T) {
	withInstantBoardExitRecheck(t)
	comments := []tracker.Comment{
		cmt(PlanningStartedHeader, time.Unix(1, 0)),
		cmt(PlanCommentHeader+"\n\n# Implementation Plan: SC-1 — a thing\n", time.Unix(2, 0)),
		cmt(PlanReadyHeader, time.Unix(3, 0)),
	}
	c := &syncCommenter{comments: comments}
	commenterFor := func() (tracker.Commenter, error) { return c, nil }
	var relaunched, resets []BoardStage
	policy := retryPolicyFor(ExitDone, true, &relaunched, &resets)

	handleBoardAgentExit(context.Background(), nil,
		hookevents.Event{AgentName: "board-SC-1-planning", EventName: hookevents.EventSessionEnd},
		FailureDeps{CommenterFor: commenterFor, Reachable: alwaysReachable, Retry: policy,
			DaemonID: "d1", Logger: zerolog.Nop()})

	require.Empty(t, c.added, "a run that already posted its own handoff draws no second marker")
	require.Equal(t, []BoardStage{BoardPlanning}, resets)
}
