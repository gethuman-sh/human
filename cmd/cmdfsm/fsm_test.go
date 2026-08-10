package cmdfsm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, args ...string) map[string]any {
	t.Helper()
	cmd := BuildFSMCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute(), "human fsm %s: %s", strings.Join(args, " "), out.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), "output is not JSON: %s", out.String())
	return got
}

func runErr(t *testing.T, args ...string) error {
	t.Helper()
	cmd := BuildFSMCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	return cmd.Execute()
}

func TestMarker_ReportsWhereItMovesAnItem(t *testing.T) {
	got := run(t, "marker", "deployed")

	assert.Equal(t, "[human:deployed]", got["marker"])
	assert.Equal(t, true, got["moves_an_item"])
	assert.NotEmpty(t, got["moves"])
	// deployed must say HOW the work shipped, and either field answers it, so
	// the contract is a one-of rather than a required list. The caller is told
	// both ways out — otherwise an already-merged branch has no postable marker.
	assert.Contains(t, got["any_of_fields"], "pr", "the caller is told what the marker requires")
	assert.Contains(t, got["any_of_fields"], "merged")
}

// Either form answers: a caller says a marker the way it posts it, and the
// document writes it the way it appears on a ticket.
func TestMarker_AcceptsEitherForm(t *testing.T) {
	assert.Equal(t, run(t, "marker", "deployed"), run(t, "marker", "[human:deployed]"))
}

// A dual-role marker must not read as merely decorative: it moves an item from
// some states and records content from others, and an agent told only the second
// half would think posting it is free.
func TestMarker_DualRoleReportsBothHalves(t *testing.T) {
	got := run(t, "marker", "plan")

	assert.Equal(t, true, got["records_content"])
	assert.Equal(t, true, got["moves_an_item"])
	assert.NotEmpty(t, got["dual_role"], "and says why it is both")
}

func TestMarker_UnknownIsAnErrorRatherThanAnEmptyAnswer(t *testing.T) {
	err := runErr(t, "marker", "not-a-marker")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not mention this marker")
}

func TestConstants_CarriesTheBudgetsTheProseRefersToByName(t *testing.T) {
	got := run(t, "constants")

	constants, ok := got["constants"].(map[string]any)
	require.True(t, ok)
	for _, name := range []string{"DefaultStageRetries", "StuckRunningGrace", "MaxSilenceReaps"} {
		assert.Contains(t, constants, name)
	}
}

// where needs the daemon, and says so plainly rather than failing obscurely: it
// is the one subcommand that cannot answer from the compiled-in document alone.
func TestWhere_SaysSoWhenNoDaemonIsReachable(t *testing.T) {
	original := connectDaemon
	t.Cleanup(func() { connectDaemon = original })
	connectDaemon = func() (*daemon.Client, error) {
		return nil, assert.AnError
	}

	err := runErr(t, "where", "SC-1")

	require.Error(t, err)
}

// The placeholders in a command must survive as typed. Go's JSON encoder escapes
// angle brackets by default, and a caller pastes what it is handed.
func TestEmitDoesNotHTMLEscapePlaceholders(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, emit(&out, map[string]string{"command": "human marker post <KEY> plan-ready"}))

	assert.Contains(t, out.String(), "<KEY>")
	assert.NotContains(t, out.String(), "\\u003c", "the escaped form must not reach a caller")
}
