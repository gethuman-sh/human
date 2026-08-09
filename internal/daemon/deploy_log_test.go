package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/forge"
)

// deployLogDeps wires the deploy engine to a buffer it writes its progress into.
func deployLogDeps(t *testing.T, p *fakeDeployer, c *fakeCommenter) (BoardTransitionDeps, *bytes.Buffer) {
	t.Helper()
	orig := deployCheckInterval
	deployCheckInterval = time.Millisecond
	t.Cleanup(func() { deployCheckInterval = orig })

	var buf bytes.Buffer
	deps := newDeps(c, nil, p)
	deps.Launcher = nil
	deps.Logger = zerolog.New(&buf)
	return deps, &buf
}

// A deploy that runs to a merge writes down every step it took. Without this
// the gate is silent for as long as CI takes, so a deploy interrupted mid-wait
// — by a restart, or by a signal aimed at the daemon — is indistinguishable
// afterwards from one that never ran, and has to be reconstructed from merge
// timestamps.
func TestDeployBranchLogsItsProgress(t *testing.T) {
	p := &fakeDeployer{
		res:    PRResult{Number: 42, URL: "https://example/pr/42"},
		checks: []forge.ChecksState{forge.ChecksPending, forge.ChecksPending, forge.ChecksPassing},
	}
	deps, buf := deployLogDeps(t, p, &fakeCommenter{})

	require.NoError(t, deps.DeployBranch(context.Background(), "SC-1", "t", "body", "feat/x"))

	log := buf.String()
	for _, want := range []string{
		"deploy: queued",
		"deploy: started",
		"deploy: pull request open",
		"deploy: waiting for CI checks",
		"deploy: CI checks passed",
		"deploy: merged",
		"deploy: done",
	} {
		assert.Contains(t, log, want)
	}
	assert.Contains(t, log, `"pm":"SC-1"`)
	assert.Contains(t, log, `"branch":"feat/x"`)
	assert.Contains(t, log, `"pr":42`)
}

// The wait itself reports that it is still alive, so a long CI gate is visible
// as a running deploy rather than as nothing at all.
func TestWaitForChecksHeartbeats(t *testing.T) {
	pending := make([]forge.ChecksState, deployWaitHeartbeat+1)
	for i := range pending {
		pending[i] = forge.ChecksPending
	}
	p := &fakeDeployer{
		res:    PRResult{Number: 7, URL: "https://example/pr/7"},
		checks: append(pending, forge.ChecksPassing),
	}
	deps, buf := deployLogDeps(t, p, &fakeCommenter{})

	require.NoError(t, deps.DeployBranch(context.Background(), "SC-2", "t", "body", "feat/y"))
	assert.Contains(t, buf.String(), "deploy: CI still running")
}

// A failure says so in the log as well as on the ticket, with the actionable
// headline and none of the cause chain's newlines.
func TestDeployFailedLogsHeadlineOnly(t *testing.T) {
	p := &fakeDeployer{
		res:    PRResult{Number: 9, URL: "https://example/pr/9"},
		checks: []forge.ChecksState{forge.ChecksFailing},
	}
	deps, buf := deployLogDeps(t, p, &fakeCommenter{})

	require.Error(t, deps.DeployBranch(context.Background(), "SC-3", "t", "body", "feat/z"))

	log := buf.String()
	assert.Contains(t, log, "deploy: failed")
	// One JSON object per line: a reason carrying the cause chain would have
	// split the record across lines.
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		assert.True(t, strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}"), "log line not a single record: %q", line)
	}
}

func TestHeadlineOf(t *testing.T) {
	assert.Equal(t, "the headline", headlineOf("the headline\n\ncause: deeper cause"))
	assert.Equal(t, "no newline", headlineOf("no newline"))
	assert.Empty(t, headlineOf(""))
}
