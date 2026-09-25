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
