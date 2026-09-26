package cmdfeedback

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
)

func renderTo(t *testing.T, report daemon.FeedbackReport, raw bool) string {
	t.Helper()
	var out bytes.Buffer
	render(&out, report, raw)
	return out.String()
}

// What a person reads is what a launch prompt carries: the heading, a blank
// line, the block.
func TestRender_PrintsTheBlockAsALaunchCarriesIt(t *testing.T) {
	got := renderTo(t, daemon.FeedbackReport{Key: "SC-1", Stage: daemon.BoardPlanning, Rows: 2, Block: "- a.go [tests]: add one\n"}, false)

	assert.Equal(t, daemon.FeedbackHeading+"\n\n- a.go [tests]: add one\n", got)
}

func TestRender_SaysSoWhenThereIsNothing(t *testing.T) {
	got := renderTo(t, daemon.FeedbackReport{Key: "SC-1", Stage: daemon.BoardPlanning}, false)

	assert.Equal(t, noFeedback+"\n", got)
}

func TestRender_RawShowsWhatTheModelSaw(t *testing.T) {
	got := renderTo(t, daemon.FeedbackReport{
		Key: "SC-1", Stage: daemon.BoardImplementation, Rows: 4, Cached: true,
		Files: []string{"a.go", "b/c.go"}, Prompt: "You write a short briefing…\n", Block: "- a.go [tests]: add one",
	}, true)

	assert.Contains(t, got, "# SC-1 implementation — 4 record rows (block from the launch cache)")
	assert.Contains(t, got, "# scope: a.go b/c.go")
	assert.Contains(t, got, "You write a short briefing…")
	assert.Contains(t, got, daemon.FeedbackHeading+"\n\n- a.go [tests]: add one\n")
}

func TestFeedback_SaysSoWhenNoDaemonIsReachable(t *testing.T) {
	original := connectDaemon
	t.Cleanup(func() { connectDaemon = original })
	connectDaemon = func() (*daemon.Client, error) { return nil, assert.AnError }

	cmd := BuildFeedbackCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"SC-1", "planning"})

	require.Error(t, cmd.Execute())
}

func TestFeedback_NeedsKeyAndStage(t *testing.T) {
	cmd := BuildFeedbackCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"SC-1"})

	require.Error(t, cmd.Execute())
	assert.Contains(t, cmd.Long, "prreview", "the help lists the stages a launch briefs")
}
