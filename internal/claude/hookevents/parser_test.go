package hookevents

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude/logparser"
)

func TestParse_Empty(t *testing.T) {
	got := Parse(nil)
	assert.Empty(t, got)

	got = Parse([]byte{})
	assert.Empty(t, got)
}

func TestParse_SinglePromptSubmit(t *testing.T) {
	data := []byte(`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}`)
	got := Parse(data)
	require.Len(t, got, 1)
	snap := got["s1"]
	assert.Equal(t, logparser.StatusWorking, snap.Status)
	assert.Equal(t, "/proj", snap.Cwd)
	assert.Equal(t, "s1", snap.SessionID)
}

func TestParse_PromptThenStop(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Stop","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusReady, got["s1"].Status)
}

func TestParse_SubagentStartAndStop(t *testing.T) {
	data := []byte(
		`{"event":"SubagentStart","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:01Z"}` + "\n" +
			`{"event":"SubagentStop","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:03Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusReady, got["s1"].Status)
}

func TestParse_MultipleSessions(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/a","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Stop","session_id":"s2","cwd":"/b","timestamp":"2026-03-25T10:00:01Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 2)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
	assert.Equal(t, logparser.StatusReady, got["s2"].Status)
}

func TestParse_MalformedLinesSkipped(t *testing.T) {
	data := []byte(
		"not json\n" +
			`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			"{broken",
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
}

func TestParse_EmptySessionIDSkipped(t *testing.T) {
	data := []byte(`{"event":"UserPromptSubmit","session_id":"","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}`)
	got := Parse(data)
	assert.Empty(t, got)
}

func TestParse_CwdPreservedFromEarlierEvent(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Stop","session_id":"s1","cwd":"","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	assert.Equal(t, "/proj", got["s1"].Cwd)
}

func TestParse_LastEventAtUpdated(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Stop","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	expected := time.Date(2026, 3, 25, 10, 0, 5, 0, time.UTC)
	assert.Equal(t, expected, got["s1"].LastEventAt)
}

func TestParse_EmptyLines(t *testing.T) {
	data := []byte(
		"\n" +
			`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			"\n",
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
}

func TestParse_PermissionRequest(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"PermissionRequest","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:02Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusBlocked, got["s1"].Status)
}

func TestParse_StopFailure(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"StopFailure","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusError, got["s1"].Status)
}

func TestParse_StopFailureClearedByPrompt(t *testing.T) {
	data := []byte(
		`{"event":"StopFailure","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
}

func TestParse_NotificationIdlePrompt(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Notification","session_id":"s1","cwd":"/proj","notification_type":"idle_prompt","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusReady, got["s1"].Status)
}

func TestParse_NotificationPermissionPrompt(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Notification","session_id":"s1","cwd":"/proj","notification_type":"permission_prompt","timestamp":"2026-03-25T10:00:02Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusBlocked, got["s1"].Status)
}

func TestParse_NotificationUnknownTypeIgnored(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Notification","session_id":"s1","cwd":"/proj","notification_type":"some_other","timestamp":"2026-03-25T10:00:02Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status, "unknown notification should not change state")
}

func TestParse_SessionStartResetsState(t *testing.T) {
	data := []byte(
		`{"event":"StopFailure","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"SessionStart","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusReady, got["s1"].Status)
}

func TestParse_SessionEnd(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"SessionEnd","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusEnded, got["s1"].Status)
}

func TestParse_StopPreservesError(t *testing.T) {
	// StopFailure followed by Stop should keep error status.
	data := []byte(
		`{"event":"StopFailure","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"Stop","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:01Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusError, got["s1"].Status, "Stop should not clear error")
}

func TestApplyEvent_PreToolUseSetsCurrentTool(t *testing.T) {
	snap := &SessionSnapshot{Status: logparser.StatusWorking}
	ApplyEvent(snap, &Event{EventName: "PreToolUse", ToolName: "Bash"})
	assert.Equal(t, "Bash", snap.CurrentTool)
	assert.Equal(t, logparser.StatusWorking, snap.Status, "PreToolUse should not change status")
}

func TestApplyEvent_PostToolUseClearsAndSetsWorking(t *testing.T) {
	// Simulates: AskUserQuestion answered → PostToolUse fires
	snap := &SessionSnapshot{
		Status:      logparser.StatusWaiting,
		CurrentTool: "AskUserQuestion",
		BlockedTool: "",
	}
	ApplyEvent(snap, &Event{EventName: "PostToolUse", ToolName: "AskUserQuestion"})
	assert.Equal(t, logparser.StatusWorking, snap.Status)
	assert.Empty(t, snap.CurrentTool)
	assert.Empty(t, snap.BlockedTool)
}

func TestApplyEvent_PostToolUseFailureSetsWorking(t *testing.T) {
	snap := &SessionSnapshot{
		Status:      logparser.StatusWorking,
		CurrentTool: "Bash",
	}
	ApplyEvent(snap, &Event{EventName: "PostToolUseFailure", ToolName: "Bash"})
	assert.Equal(t, logparser.StatusWorking, snap.Status)
	assert.Empty(t, snap.CurrentTool)
}

func TestApplyEvent_PermissionRequestSetsBlockedTool(t *testing.T) {
	snap := &SessionSnapshot{
		Status:      logparser.StatusWorking,
		CurrentTool: "Bash",
	}
	ApplyEvent(snap, &Event{EventName: "PermissionRequest", ToolName: "Bash"})
	assert.Equal(t, logparser.StatusBlocked, snap.Status)
	assert.Equal(t, "Bash", snap.BlockedTool)
	assert.Empty(t, snap.CurrentTool)
}

func TestApplyEvent_StopFailureSetsErrorType(t *testing.T) {
	snap := &SessionSnapshot{Status: logparser.StatusWorking, CurrentTool: "Bash"}
	ApplyEvent(snap, &Event{EventName: "StopFailure", ErrorType: "rate_limit"})
	assert.Equal(t, logparser.StatusError, snap.Status)
	assert.Equal(t, "rate_limit", snap.ErrorType)
	assert.Empty(t, snap.CurrentTool)
}

func TestApplyEvent_UserPromptClearsAll(t *testing.T) {
	snap := &SessionSnapshot{
		Status:      logparser.StatusError,
		ErrorType:   "rate_limit",
		BlockedTool: "Bash",
		CurrentTool: "Read",
	}
	ApplyEvent(snap, &Event{EventName: "UserPromptSubmit"})
	assert.Equal(t, logparser.StatusWorking, snap.Status)
	assert.Empty(t, snap.ErrorType)
	assert.Empty(t, snap.BlockedTool)
	assert.Empty(t, snap.CurrentTool)
}

func TestApplyEvent_StopClearsToolFields(t *testing.T) {
	snap := &SessionSnapshot{
		Status:      logparser.StatusWorking,
		CurrentTool: "Bash",
		BlockedTool: "Edit",
	}
	ApplyEvent(snap, &Event{EventName: "Stop"})
	assert.Equal(t, logparser.StatusReady, snap.Status)
	assert.Empty(t, snap.CurrentTool)
	assert.Empty(t, snap.BlockedTool)
}

func TestApplyEvent_SessionStartClearsAll(t *testing.T) {
	snap := &SessionSnapshot{
		Status:      logparser.StatusError,
		ErrorType:   "billing_error",
		CurrentTool: "Bash",
		BlockedTool: "Write",
	}
	ApplyEvent(snap, &Event{EventName: "SessionStart"})
	assert.Equal(t, logparser.StatusReady, snap.Status)
	assert.Empty(t, snap.ErrorType)
	assert.Empty(t, snap.CurrentTool)
	assert.Empty(t, snap.BlockedTool)
}

func TestParse_FullToolLifecycle(t *testing.T) {
	// PreToolUse → PostToolUse should show and clear current tool.
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"PreToolUse","session_id":"s1","cwd":"/proj","tool_name":"Bash","timestamp":"2026-03-25T10:00:01Z"}` + "\n" +
			`{"event":"PostToolUse","session_id":"s1","cwd":"/proj","tool_name":"Bash","timestamp":"2026-03-25T10:00:02Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
	assert.Empty(t, got["s1"].CurrentTool, "PostToolUse should clear CurrentTool")
}

func TestApplyEvent_PreToolUseExitPlanModeSetsWaiting(t *testing.T) {
	snap := &SessionSnapshot{Status: logparser.StatusWorking}
	ApplyEvent(snap, &Event{EventName: "PreToolUse", ToolName: "ExitPlanMode"})
	assert.Equal(t, logparser.StatusWaiting, snap.Status)
	assert.Empty(t, snap.CurrentTool, "waiting tools should not show as current tool")
}

func TestApplyEvent_PreToolUseAskUserQuestionSetsWaiting(t *testing.T) {
	snap := &SessionSnapshot{Status: logparser.StatusWorking}
	ApplyEvent(snap, &Event{EventName: "PreToolUse", ToolName: "AskUserQuestion"})
	assert.Equal(t, logparser.StatusWaiting, snap.Status)
	assert.Empty(t, snap.CurrentTool)
}

func TestParse_ExitPlanModeWaitingThenResumes(t *testing.T) {
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"PreToolUse","session_id":"s1","cwd":"/proj","tool_name":"ExitPlanMode","timestamp":"2026-03-25T10:00:01Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWaiting, got["s1"].Status)
	assert.Empty(t, got["s1"].CurrentTool)

	// User responds, PostToolUse fires.
	data = append(data, []byte("\n"+
		`{"event":"PostToolUse","session_id":"s1","cwd":"/proj","tool_name":"ExitPlanMode","timestamp":"2026-03-25T10:00:05Z"}`)...)
	got = Parse(data)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
}

func TestParse_BlockedThenPostToolUseResumes(t *testing.T) {
	// PermissionRequest → PostToolUse (permission granted, tool ran).
	data := []byte(
		`{"event":"UserPromptSubmit","session_id":"s1","cwd":"/proj","timestamp":"2026-03-25T10:00:00Z"}` + "\n" +
			`{"event":"PermissionRequest","session_id":"s1","cwd":"/proj","tool_name":"Bash","timestamp":"2026-03-25T10:00:01Z"}` + "\n" +
			`{"event":"PostToolUse","session_id":"s1","cwd":"/proj","tool_name":"Bash","timestamp":"2026-03-25T10:00:05Z"}`,
	)
	got := Parse(data)
	require.Len(t, got, 1)
	assert.Equal(t, logparser.StatusWorking, got["s1"].Status)
	assert.Empty(t, got["s1"].BlockedTool)
}
