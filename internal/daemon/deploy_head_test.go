package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
)

// fakeForge409 is the verbatim refusal SC-5326's deploy drew from GitHub
// (comment 5363): a 409 whose message says the head moved, carrying the
// status as structured detail exactly as internal/apiclient stamps it.
func fakeForge409() error {
	return errors.WithDetails(
		`github PUT /repos/gethuman-sh/human/pulls/559/merge returned 409: `+
			`{"message":"Head branch is out of date. Review and try the merge again.","status":"409"}`,
		"statusCode", 409)
}

// TestIsTransientMergeRefusal_HeadOutOfDate409 pins the defect: a 409 "head is
// out of date" is the forge saying the head moved under us — the one refusal
// that clears by re-integrating, not a terminal conflict (SC-5395).
func TestIsTransientMergeRefusal_HeadOutOfDate409(t *testing.T) {
	assert.True(t, isTransientMergeRefusal(fakeForge409()),
		"a 409 head-out-of-date is the forge saying the head moved under us — the one refusal "+
			"that clears by re-integrating, not a terminal conflict (SC-5395)")
}

// TestIsTransientMergeRefusal_DraftStaysTerminal pins that the SC-4027
// carve-out survives the 409 rewrite: a draft refusal is also a 405 and must
// stay terminal.
func TestIsTransientMergeRefusal_DraftStaysTerminal(t *testing.T) {
	err := errors.WithDetails("pull request was not merged: ... is still a draft", "statusCode", 405)
	assert.False(t, isTransientMergeRefusal(err))
}

// TestIsTransientMergeRefusal_UnrelatedStatusIsTerminal: a status outside
// {405,409} is terminal even though the message says "not mergeable" is
// absent.
func TestIsTransientMergeRefusal_UnrelatedStatusIsTerminal(t *testing.T) {
	err := errors.WithDetails("... returned 422: ...", "statusCode", 422)
	assert.False(t, isTransientMergeRefusal(err))
}

// TestDeployBranch_LaggingForgeHead_NeverGatesOnTheReplacedTip is the SC-5395
// regression for defect A: after the freshness rebase publishes a new head,
// the forge's pull-request resource still reports the replaced tip for a
// beat. The re-gate must wait for the forge to report the PUBLISHED head
// before reading any check verdict from it — never gate on the replaced tip.
func TestDeployBranch_LaggingForgeHead_NeverGatesOnTheReplacedTip(t *testing.T) {
	syncDeploy(t)
	origHeadInterval, origHeadTimeout := headPollInterval, deployHeadWaitTimeout
	origMergeInterval, origMergeTimeout := mergeRetryInterval, mergeRetryTimeout
	origMergeablePollInterval, origMergeablePollTimeout := mergeablePollInterval, mergeablePollTimeout
	headPollInterval, deployHeadWaitTimeout = time.Millisecond, time.Second
	mergeRetryInterval, mergeRetryTimeout = time.Millisecond, time.Second
	mergeablePollInterval, mergeablePollTimeout = time.Millisecond, time.Second
	t.Cleanup(func() {
		headPollInterval, deployHeadWaitTimeout = origHeadInterval, origHeadTimeout
		mergeRetryInterval, mergeRetryTimeout = origMergeInterval, origMergeTimeout
		mergeablePollInterval, mergeablePollTimeout = origMergeablePollInterval, origMergeablePollTimeout
	})

	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{
		res:          PRResult{Number: 59, URL: "https://example/pr/59"},
		checks:       []forge.ChecksState{forge.ChecksPassing},
		mergeable:    true,
		preHead:      "0aa7fe89",
		head:         "50358b7b",
		forgeHeadLag: 2,
		// The refusal is what makes this test discriminate: the fake merges only
		// once the head it SERVES is the published one, which happens only if the
		// gate waited for it. Without it the pre-fix control flow (no head read at
		// all) satisfies every assertion here and the regression cannot fail.
		mergeRefusalUntilHeadCurrent: fakeForge409(),
	}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.Zero(t, p.checksReadAtStaleHead,
		"the post-rebase gate must never read a check verdict while the forge still reports the replaced tip")
	assert.Equal(t, 1, p.merged)
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "must not red the card: %q", b)
	}
}

// TestDeployBranch_HeadOutOfDate_ReintegratesAndMerges is the SC-5395
// regression for the acceptance criterion "two branches deployed within a
// minute of each other both merge": a 409 that survives the merge-retry
// window means a sibling deploy's merge genuinely moved the base, and the
// deploy must re-integrate (re-run the freshness rebase) and re-gate CI on
// the newly published head rather than dead-ending the card.
func TestDeployBranch_HeadOutOfDate_ReintegratesAndMerges(t *testing.T) {
	syncDeploy(t)
	origHeadInterval, origHeadTimeout := headPollInterval, deployHeadWaitTimeout
	origMergeInterval, origMergeTimeout := mergeRetryInterval, mergeRetryTimeout
	origMergeablePollInterval, origMergeablePollTimeout := mergeablePollInterval, mergeablePollTimeout
	headPollInterval, deployHeadWaitTimeout = time.Millisecond, time.Second
	mergeRetryInterval, mergeRetryTimeout = time.Millisecond, 20*time.Millisecond
	mergeablePollInterval, mergeablePollTimeout = time.Millisecond, time.Second
	t.Cleanup(func() {
		headPollInterval, deployHeadWaitTimeout = origHeadInterval, origHeadTimeout
		mergeRetryInterval, mergeRetryTimeout = origMergeInterval, origMergeTimeout
		mergeablePollInterval, mergeablePollTimeout = origMergeablePollInterval, origMergeablePollTimeout
	})

	c := &fakeCommenter{comments: deployFixReadyComments()}
	p := &fakeDeployer{
		res:                          PRResult{Number: 60, URL: "https://example/pr/60"},
		checks:                       []forge.ChecksState{forge.ChecksPassing},
		mergeable:                    true,
		head:                         "50358b7b",
		secondHead:                   "9c1d2e3f",
		refuseMergeUntilEnsured:      2,
		mergeRefusalUntilHeadCurrent: fakeForge409(),
	}
	deps := newDeps(c, &fakeLauncher{}, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, p.ensured, 2,
		"a 409 that survives the retry window means the base genuinely moved — re-integrate, do not re-issue the same merge")
	assert.GreaterOrEqual(t, p.checksPassed, 2,
		"each re-integration re-gates CI on the head it published")
	for _, b := range c.added {
		assert.False(t, strings.HasPrefix(b, DeployFailedHeader), "must not red the card: %q", b)
	}
}

// TestDeployBranch_ForgeNeverReportsPublishedHead_RedsWithoutFixer: a forge
// whose pull-request resource never catches up to the published head is not
// something a code fixer can repair — the deploy must fail plainly, without
// dispatching a fixer.
func TestDeployBranch_ForgeNeverReportsPublishedHead_RedsWithoutFixer(t *testing.T) {
	syncDeploy(t)
	origHeadInterval, origHeadTimeout := headPollInterval, deployHeadWaitTimeout
	headPollInterval, deployHeadWaitTimeout = time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { headPollInterval, deployHeadWaitTimeout = origHeadInterval, origHeadTimeout })

	c := &fakeCommenter{comments: deployFixReadyComments()}
	l := &fakeLauncher{}
	p := &fakeDeployer{
		res:          PRResult{Number: 61, URL: "https://example/pr/61"},
		checks:       []forge.ChecksState{forge.ChecksPassing},
		preHead:      "0aa7fe89",
		head:         "50358b7b",
		forgeHeadLag: 1000,
	}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
	}
	require.NotEmpty(t, failed)
	assert.Contains(t, failed, "did not report the rebased head")
	assert.Zero(t, l.calls, "a forge that never moved its PR resource is not something a code fixer can repair")
	assert.Zero(t, p.merged)
}
