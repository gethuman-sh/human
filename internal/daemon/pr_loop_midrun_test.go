package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SC-4026. The hook fires StopFailure on a model API error and Claude Code
// retries through it, so the run carries on. Driving the loop on that event reads
// a verdict the step has not written yet and reds a review that is still working
// — measured on SC-3613, where a server_error escalated the card nine seconds
// after it arrived and the reviewer went on to approve 83 seconds later.
func TestDrivePRLoopExit_SubstrateFailureIsNotAnExit(t *testing.T) {
	transient := []string{"server_error", "api_error", "overloaded", "rate_limit"}
	for _, errorType := range transient {
		t.Run(errorType, func(t *testing.T) {
			var advanced []string
			var reclaimed []string

			handled := drivePRLoopExit("SC-1", prReviewAgentStage, "board-SC-1-prreview", errorType,
				func(pmKey, _, _ string) error { advanced = append(advanced, pmKey); return nil },
				func(name string) { reclaimed = append(reclaimed, name) },
				zerolog.Nop())

			assert.True(t, handled, "the event belongs to the loop and is consumed here")
			assert.Empty(t, advanced, "a run that is still retrying has not produced an outcome to act on")
			// The fixer's deliverable is an unpushed local commit; the handoff flag
			// authorizes removing its worktree, so it must not be flipped on an error
			// the run then recovers from.
			assert.Empty(t, reclaimed, "the worktree stays protected while the run continues")
		})
	}
}

// The other half: a genuine exit still drives the loop. Without this the fix
// would be indistinguishable from switching the loop off.
func TestDrivePRLoopExit_RealExitStillDrivesTheLoop(t *testing.T) {
	for _, errorType := range []string{"", "unrecognised_thing"} {
		t.Run("errorType="+errorType, func(t *testing.T) {
			var advanced []string
			var reclaimed []string

			handled := drivePRLoopExit("SC-1", prFixAgentStage, "board-SC-1-prfix", errorType,
				func(pmKey, _, _ string) error { advanced = append(advanced, pmKey); return nil },
				func(name string) { reclaimed = append(reclaimed, name) },
				zerolog.Nop())

			assert.True(t, handled)
			assert.Equal(t, []string{"SC-1"}, advanced)
			assert.Equal(t, []string{"board-SC-1-prfix"}, reclaimed)
		})
	}
}

// A single run can produce two events that both look like its exit: the hook
// fires StopFailure on an API error and Stop when the turn ends, and a Stop may
// follow a StopFailure by contract. On SC-3613 that posted the identical
// escalation twice, sixteen seconds apart. Nothing moved in between, so the card
// must say it once.
func TestEscalatePRLoop_DoesNotRepeatItself(t *testing.T) {
	c := &fakeCommenter{comments: reviewStartedComments(1, "https://example/pr/7", 7, "feat/x")}
	deps := newDeps(c, &fakeLauncher{}, &fakeDeployer{})
	outcome := PRLoopOutcome{ReviewRecorded: false}

	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", outcome))
	require.NoError(t, deps.AdvancePRLoop(context.Background(), "SC-1", outcome))

	failures := 0
	for _, cm := range c.comments {
		if strings.HasPrefix(strings.TrimSpace(cm.Body), PRReviewFailedHeader) {
			failures++
		}
	}
	assert.Equal(t, 1, failures, "the second drive of the same dead round adds nothing to say")
}
