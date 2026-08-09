package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
)

// A newer Claude Code renamed the event key; the daemon is unreachable.
// The forwarder must (a) still recover and forward the event and
// (b) surface the delivery failure on stderr — never exit silently.
func TestRunHook_RenamedEventKey_UnreachableDaemon(t *testing.T) {
	payload := `{"eventName":"Stop","session_id":"s1","cwd":"/w","tool_name":""}`

	var captured []string
	deliver := func(args []string) error {
		captured = args
		return errors.WithDetails("cannot reach daemon", "addr", "127.0.0.1:1")
	}

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err, "hook must never fail the calling process")
	require.NotEmpty(t, captured, "event must be forwarded even under a renamed key")
	require.Equal(t, "hook-event", captured[0])
	assert.Equal(t, "Stop", captured[1], "event name must be recovered from an aliased key")
	assert.NotEmpty(t, stderr.String(), "a failed delivery must leave a visible diagnostic")
	assert.Contains(t, stderr.String(), "Stop")
}

// A body with no recognizable event key must warn on stderr, not vanish.
func TestRunHook_UnknownEventKey_WarnsAndDropsWithoutDelivery(t *testing.T) {
	payload := `{"totally_unknown":"x"}`

	delivered := false
	deliver := func([]string) error { delivered = true; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err)
	assert.False(t, delivered, "no event name means nothing to deliver")
	assert.NotEmpty(t, stderr.String(), "an unrecognized non-empty body must warn on stderr")
}

// Canonical current-schema payload still works and forwards all fields.
func TestRunHook_CanonicalKey_ForwardsSuccessfully(t *testing.T) {
	payload := `{"hook_event_name":"PostToolUse","session_id":"s2","cwd":"/w","tool_name":"Bash"}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err)
	require.Len(t, captured, 12)
	assert.Equal(t, "PostToolUse", captured[1])
	assert.Equal(t, "s2", captured[2])
	assert.Equal(t, "/w", captured[3])
	assert.Equal(t, "Bash", captured[5])
	assert.Empty(t, stderr.String(), "a successful delivery must stay quiet")
}

// The run id rides back on every event so the daemon can recognise its own work
// (SC-4082). It is appended LAST: the daemon parses positionally, so a container
// running an older CLI must keep parsing cleanly, and a new field can only go on
// the end.
func TestRunHook_ForwardsTheRunID(t *testing.T) {
	t.Setenv("HUMAN_RUN_ID", "run-abc123")
	payload := `{"hook_event_name":"Stop","session_id":"s3","cwd":"/w"}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	require.NoError(t, runHook(strings.NewReader(payload), &bytes.Buffer{}, deliver))
	require.Len(t, captured, 12)
	assert.Equal(t, "run-abc123", captured[11])
}

// Outside a daemon-launched container there is no run id, and the event must
// still be delivered — the daemon has an explicit no-id path for exactly this.
func TestRunHook_NoRunIDStillForwards(t *testing.T) {
	t.Setenv("HUMAN_RUN_ID", "")
	payload := `{"hook_event_name":"Stop","session_id":"s4","cwd":"/w"}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	require.NoError(t, runHook(strings.NewReader(payload), &bytes.Buffer{}, deliver))
	require.Len(t, captured, 12)
	assert.Empty(t, captured[11])
}

// A PreToolUse payload carrying a Bash command must forward the command, not
// merely the tool name. Regression for SC-2461: the record must answer what a
// tool ran, not only that it ran.
func TestRunHook_CapturesToolInputCommand(t *testing.T) {
	payload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w",` +
		`"tool_name":"Bash","tool_input":{"command":"go test ./internal/recall/... -run TestOverlap"}}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err)
	require.NotEmpty(t, captured)
	joined := strings.Join(captured, "\x00")
	assert.Contains(t, joined, "go test ./internal/recall/... -run TestOverlap",
		"the forwarded record must carry the tool's command, not just its name")
}

// An empty stdin invocation is a genuine no-op — no warning noise.
func TestRunHook_EmptyBody_NoWarnNoDeliver(t *testing.T) {
	delivered := false
	deliver := func([]string) error { delivered = true; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader("   \n"), &stderr, deliver)

	require.NoError(t, err)
	assert.False(t, delivered)
	assert.Empty(t, stderr.String())
}

// SC-3582 regression: Claude Code nests the sub-agent attribution inside
// tool_input for an Agent spawn. This test pins that payload shape — if the
// forwarder ever reads the wrong level again, the two fields go empty here.
func TestRunHook_AgentSpawn_CapturesNestedAttribution(t *testing.T) {
	payload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w",` +
		`"tool_name":"Agent","tool_input":{"description":"Fix PR review findings",` +
		`"prompt":"Fix the findings on SC-1","subagent_type":"human-pr-fixer","model":"sonnet"}}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	require.NoError(t, runHook(strings.NewReader(payload), &stderr, deliver))
	require.Len(t, captured, 12)
	assert.Equal(t, "human-pr-fixer", captured[9], "sub-agent type must come from tool_input")
	assert.Equal(t, "sonnet", captured[10], "model must come from tool_input")
}

// The attribution keys trail a real dispatch's prompt. Extraction must happen
// before the daemon's 1 KiB cap, so a long prompt may not push them out of reach.
func TestRunHook_AgentSpawn_LongPromptBeforeAttribution(t *testing.T) {
	longPrompt := strings.Repeat("a", 5000)
	payload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w",` +
		`"tool_name":"Agent","tool_input":{"prompt":"` + longPrompt + `",` +
		`"subagent_type":"human-planner","model":"opus"}}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	require.NoError(t, runHook(strings.NewReader(payload), &stderr, deliver))
	require.Len(t, captured, 12)
	assert.Equal(t, "human-planner", captured[9])
	assert.Equal(t, "opus", captured[10])
}

// A spawn that names no model inherits its parent's; that must be recorded as
// a fact, not left empty where it would read as "never captured".
func TestRunHook_AgentSpawn_NoModel_RecordsInherited(t *testing.T) {
	payload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w",` +
		`"tool_name":"Agent","tool_input":{"subagent_type":"human-executor","prompt":"go"}}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	require.NoError(t, runHook(strings.NewReader(payload), &stderr, deliver))
	require.Len(t, captured, 12)
	assert.Equal(t, "human-executor", captured[9])
	assert.Equal(t, hookevents.ModelInherited, captured[10])
}

// SessionStart is the event kind that carries model at the top level; it must
// keep working, because the nested read is an addition, not a replacement.
func TestRunHook_TopLevelModel_StillForwarded(t *testing.T) {
	payload := `{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/w","model":"opus"}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	require.NoError(t, runHook(strings.NewReader(payload), &stderr, deliver))
	require.Len(t, captured, 12)
	assert.Empty(t, captured[9])
	assert.Equal(t, "opus", captured[10], "a top-level model must survive the nested read")
}

// An ordinary tool call is not a spawn: both fields stay empty, which is what
// downstream reads as "not captured" rather than as an inheriting spawn.
func TestRunHook_NonSpawnTool_LeavesAttributionEmpty(t *testing.T) {
	payload := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/w",` +
		`"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	require.NoError(t, runHook(strings.NewReader(payload), &stderr, deliver))
	require.Len(t, captured, 12)
	assert.Empty(t, captured[9])
	assert.Empty(t, captured[10])
}
