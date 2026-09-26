package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	humanerrors "github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
)

// SC-5843: a head red ONLY on checks no code change can turn green — the CLA
// Assistant job of PR #557 — must not be classified as a fixable CI failure.
// Before the fix ciFailureFixable returns true here and a deploy-fixer round is
// spent on a branch with nothing to repair.
func TestWaitForChecks_RedOnlyOnExternalChecks_IsNotFixable(t *testing.T) {
	// deployCheckInterval is pinned too, not just the grace: leaving it at its
	// 30s default makes the grace check race the wall clock (time.Since taken
	// in the same statement as the clock start can read under 1ms), so the
	// test would otherwise wait a full tick before the grace reads elapsed.
	origInterval, origGrace := deployCheckInterval, deployExternalCheckGrace
	deployCheckInterval, deployExternalCheckGrace = time.Millisecond, time.Millisecond
	t.Cleanup(func() { deployCheckInterval, deployExternalCheckGrace = origInterval, origGrace })

	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "build", Conclusion: forge.ChecksPassing},
			{Name: "test", Conclusion: forge.ChecksPassing},
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	err := deps.waitForChecks(context.Background(), PRResult{Number: 557, URL: "https://example/pr/557"}, "")
	require.Error(t, err)
	assert.False(t, ciFailureFixable(err),
		"a contributor-signature check is not a code defect; no fixer round may be spent on it")
	headline := ciFailureHeadline(err)
	assert.Contains(t, headline, "cla")
	assert.Contains(t, headline, "no code change can turn green")
	assert.NotContains(t, headline, "fix the failing checks")
}

// The mixed head still dispatches the fixer, and the marker names only the
// checks a code change can turn green — a fixer told to fix "cla" looks for a
// defect that is not in the branch.
func TestWaitForChecks_MixedRed_NamesOnlyCodeFixableChecks(t *testing.T) {
	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "build", Conclusion: forge.ChecksFailing},
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	err := deps.waitForChecks(context.Background(), PRResult{Number: 21, URL: "https://example/pr/21"}, "")
	require.Error(t, err)
	assert.True(t, ciFailureFixable(err), "a red build is still a code defect")
	names, _ := humanerrors.AllDetails(err)[deployFailingChecksDetail].(string)
	assert.Equal(t, "build", names)
}

// Within the grace an all-external red is waited out, not failed: bot checks
// routinely re-run, and the gate must see the re-run before it judges the head.
func TestWaitForChecks_ExternalRedWithinGrace_WaitsForTheRerun(t *testing.T) {
	origInterval, origGrace := deployCheckInterval, deployExternalCheckGrace
	deployCheckInterval, deployExternalCheckGrace = time.Millisecond, time.Hour
	t.Cleanup(func() { deployCheckInterval, deployExternalCheckGrace = origInterval, origGrace })

	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing, forge.ChecksFailing, forge.ChecksPassing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	require.NoError(t, deps.waitForChecks(context.Background(), PRResult{Number: 30, URL: "https://example/pr/30"}, ""))
	assert.Equal(t, 3, p.checkCall, "the gate polled through the external red until it cleared")
}

// An unknown check name stays fixable, so a real build break can never be
// silenced by this carve-out — and the match is exact, never a substring.
func TestWaitForChecks_UnknownAndClaLikeNamesStayFixable(t *testing.T) {
	for _, name := range []string{"integration", "cla-format-lint", "build (cla)"} {
		p := &fakeDeployer{
			checks: []forge.ChecksState{forge.ChecksFailing},
			prState: &forge.PullRequestState{Checks: []forge.CheckResult{
				{Name: name, Conclusion: forge.ChecksFailing},
			}},
		}
		deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
		err := deps.waitForChecks(context.Background(), PRResult{Number: 31, URL: "https://example/pr/31"}, "")
		require.Error(t, err)
		assert.True(t, ciFailureFixable(err), "%s must stay code-fixable", name)
	}
}

// End to end on the engine: the real incident's head reds the card with the
// check named and a re-run remedy, and dispatches NO fixer even with a launcher
// wired and the full round budget free.
func TestDeployBranch_ExternalCheckRed_RedsWithoutAFixer(t *testing.T) {
	syncDeploy(t)
	origGrace := deployExternalCheckGrace
	deployExternalCheckGrace = time.Millisecond
	t.Cleanup(func() { deployExternalCheckGrace = origGrace })

	c := &fakeCommenter{comments: deployFixReadyComments()}
	l := &fakeLauncher{}
	p := &fakeDeployer{
		res:    PRResult{Number: 557, URL: "https://example/pr/557"},
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "build", Conclusion: forge.ChecksPassing},
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(c, l, p)
	err := deployVia(t, deps, BoardTransitionRequest{PMKey: "SC-1", From: BoardVerification, To: BoardDoneStage})
	require.Error(t, err)

	var failed string
	for _, b := range c.added {
		if strings.HasPrefix(b, DeployFailedHeader) {
			failed = b
		}
		assert.False(t, strings.HasPrefix(b, DeployFixStartedHeader),
			"no fixer round may be spent on a check no code change can turn green: %q", b)
	}
	require.NotEmpty(t, failed)
	assert.Contains(t, failed, "cla")
	assert.NotContains(t, failed, "fix the failing checks")
	assert.Zero(t, l.calls)
	assert.Zero(t, p.merged, "a red head is still not merged")
}

// The other signal: a check whose NAME is unknown but whose owning app is a
// known signature bot is external too — the GitHub-App form of the same bot.
func TestWaitForChecks_ExternalByAppSlug(t *testing.T) {
	// See TestWaitForChecks_RedOnlyOnExternalChecks_IsNotFixable: pin the poll
	// interval too, or the grace check races the wall clock.
	origInterval, origGrace := deployCheckInterval, deployExternalCheckGrace
	deployCheckInterval, deployExternalCheckGrace = time.Millisecond, time.Millisecond
	t.Cleanup(func() { deployCheckInterval, deployExternalCheckGrace = origInterval, origGrace })

	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "license/agreement", Conclusion: forge.ChecksFailing, App: "cla-assistant"},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	err := deps.waitForChecks(context.Background(), PRResult{Number: 32, URL: "https://example/pr/32"}, "")
	require.Error(t, err)
	assert.False(t, ciFailureFixable(err))
	assert.Contains(t, ciFailureHeadline(err), "license/agreement")
}

// SC-5843 r1 (blocking, board review): combineChecks reds the aggregate on the
// FIRST failing check without waiting for the ones still pending, so a head
// with build/test still running and only cla concluded reaches redHeadVerdict
// as ChecksFailing before the code checks have answered. Past the grace this
// must keep polling, not declare "no code change can turn green" on a build
// that has not finished — the exact probe from PR #557's shape once build/test
// are still in flight: {build: pending, test: pending, cla: failing}.
func TestRedHeadVerdict_PendingCodeCheckIsNotYetConclusive(t *testing.T) {
	p := &fakeDeployer{
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "build", Conclusion: forge.ChecksPending},
			{Name: "test", Conclusion: forge.ChecksPending},
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	// graceElapsed=true models the probe exactly: five minutes have passed and
	// the classifier is still asked for a verdict.
	err := deps.redHeadVerdict(PRResult{Number: 557, URL: "https://example/pr/557"}, 300, "", true)
	assert.NoError(t, err,
		"a head with a still-pending code check must not be judged an external-only red just because the grace elapsed")
}

// End to end: waitForChecks must keep polling a head whose only concluded
// failure is external while build/test are still pending, all the way to the
// deploy's own bounded wait — never redding the card with the external-only
// "re-run those checks" verdict while a possibly-broken build has not finished.
func TestWaitForChecks_PendingCodeCheckOutlivesTheExternalGrace(t *testing.T) {
	origInterval, origGrace := deployCheckInterval, deployExternalCheckGrace
	deployCheckInterval, deployExternalCheckGrace = time.Millisecond, time.Millisecond
	t.Cleanup(func() { deployCheckInterval, deployExternalCheckGrace = origInterval, origGrace })

	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "build", Conclusion: forge.ChecksPending},
			{Name: "test", Conclusion: forge.ChecksPending},
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := deps.waitForChecks(ctx, PRResult{Number: 557, URL: "https://example/pr/557"}, "")
	require.Error(t, err)
	assert.False(t, externalChecksRed(err),
		"still-pending build/test must not be judged external-only just because the grace elapsed")
	assert.Contains(t, err.Error(), "timed out waiting for CI checks",
		"the head must still be waiting (bounded by the deploy's own timeout), not failed on the bot's answer alone")
}

// SC-5843 r1 (non-blocking, same review): a failing check the classifier
// cannot attribute to a name must keep the head code-fixable, even sitting
// beside a named external failure — {unnamed: failing, cla: failing} must not
// read as "all failing checks are external".
func TestWaitForChecks_UnattributableFailingCheckStaysFixable(t *testing.T) {
	origInterval, origGrace := deployCheckInterval, deployExternalCheckGrace
	deployCheckInterval, deployExternalCheckGrace = time.Millisecond, time.Millisecond
	t.Cleanup(func() { deployCheckInterval, deployExternalCheckGrace = origInterval, origGrace })

	p := &fakeDeployer{
		checks: []forge.ChecksState{forge.ChecksFailing},
		prState: &forge.PullRequestState{Checks: []forge.CheckResult{
			{Name: "", Conclusion: forge.ChecksFailing},
			{Name: "cla", Conclusion: forge.ChecksFailing},
		}},
	}
	deps := newDeps(&fakeCommenter{}, &fakeLauncher{}, p)
	err := deps.waitForChecks(context.Background(), PRResult{Number: 40, URL: "https://example/pr/40"}, "")
	require.Error(t, err)
	assert.True(t, ciFailureFixable(err),
		"an unattributable failing check must keep the head code-fixable, per the unknown-stays-fixable rule")
}

// checkIsExternal matches exactly, by either signal, and nothing else.
func TestCheckIsExternal(t *testing.T) {
	assert.True(t, checkIsExternal(forge.CheckResult{Name: "cla"}))
	assert.True(t, checkIsExternal(forge.CheckResult{Name: " CLA "}))
	assert.True(t, checkIsExternal(forge.CheckResult{Name: "anything", App: "CLA-Assistant"}))
	assert.False(t, checkIsExternal(forge.CheckResult{Name: "cla-format-lint"}))
	assert.False(t, checkIsExternal(forge.CheckResult{Name: "build", App: "github-actions"}))
	assert.False(t, checkIsExternal(forge.CheckResult{}))
}
