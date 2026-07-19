package logparser

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test helpers ---

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	return parsed
}

func marshalLine(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func makeUserEntry(t *testing.T, sessionID, cwd, slug, text, timestamp string) []byte {
	t.Helper()
	return marshalLine(t, map[string]any{
		"type":      "user",
		"sessionId": sessionID,
		"cwd":       cwd,
		"slug":      slug,
		"timestamp": timestamp,
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	})
}

func makeAssistantEntry(t *testing.T, sessionID, timestamp string, stopReason *string, content []map[string]any) []byte {
	t.Helper()
	msg := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if stopReason != nil {
		msg["stop_reason"] = *stopReason
	}
	return marshalLine(t, map[string]any{
		"type":      "assistant",
		"sessionId": sessionID,
		"timestamp": timestamp,
		"message":   msg,
	})
}

func makeToolResultEntry(t *testing.T, sessionID, timestamp, toolUseID string, toolUseResult any) []byte {
	t.Helper()
	entry := map[string]any{
		"type":      "user",
		"sessionId": sessionID,
		"timestamp": timestamp,
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": toolUseID, "content": "ok"},
			},
		},
	}
	if toolUseResult != nil {
		entry["toolUseResult"] = toolUseResult
	}
	return marshalLine(t, entry)
}

func makeProgressEntry(t *testing.T, sessionID, timestamp, parentToolUseID, agentID string) []byte {
	t.Helper()
	return marshalLine(t, map[string]any{
		"type":            "progress",
		"sessionId":       sessionID,
		"timestamp":       timestamp,
		"parentToolUseID": parentToolUseID,
		"data": map[string]any{
			"type":    "agent_progress",
			"agentId": agentID,
		},
	})
}

func joinLines(lines ...[]byte) []byte {
	var parts []string
	for _, l := range lines {
		parts = append(parts, string(l))
	}
	return []byte(strings.Join(parts, "\n") + "\n")
}

// --- tests ---

func TestFileParser_EmptyFile(t *testing.T) {
	p := NewFileParser()
	state, err := p.UpdateBytes([]byte{})
	require.NoError(t, err)
	assert.Equal(t, "", state.SessionID)
	assert.True(t, state.StartedAt.IsZero())
	assert.Equal(t, StatusReady, state.Status)
	assert.Empty(t, state.Subagents)
	assert.Empty(t, state.Tasks)
}

func TestFileParser_BasicSession(t *testing.T) {
	p := NewFileParser()
	data := joinLines(
		makeUserEntry(t, "sess-1", "/home/user/project", "cool-slug", "hello", "2026-03-25T10:00:00.000Z"),
	)

	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", state.SessionID)
	assert.Equal(t, "/home/user/project", state.Cwd)
	assert.Equal(t, "cool-slug", state.Slug)
	assert.Equal(t, ts(t, "2026-03-25T10:00:00.000Z"), state.StartedAt)
	assert.Equal(t, StatusWorking, state.Status) // user entry means Claude will work
}

func TestFileParser_Incremental(t *testing.T) {
	p := NewFileParser()

	line1 := makeUserEntry(t, "sess-1", "/project", "", "hi", "2026-03-25T10:00:00.000Z")
	data1 := joinLines(line1)

	state, err := p.UpdateBytes(data1)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", state.SessionID)
	assert.Equal(t, StatusWorking, state.Status)

	// Second batch: assistant with end_turn.
	endTurn := new("end_turn")
	line2 := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:05.000Z", endTurn, []map[string]any{
		{"type": "text", "text": "done"},
	})
	data2 := joinLines(line2)

	state, err = p.UpdateBytes(data2)
	require.NoError(t, err)
	assert.Equal(t, StatusReady, state.Status) // end_turn → idle
}

func TestFileParser_SubagentLifecycle(t *testing.T) {
	p := NewFileParser()

	// Agent tool_use.
	toolUse := new("tool_use")
	agentLine := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type": "tool_use",
			"id":   "toolu_agent1",
			"name": "Agent",
			"input": map[string]any{
				"description":   "Research codebase",
				"subagent_type": "Explore",
				"prompt":        "Find all Go files",
			},
		},
	})

	// Progress entry with agentId.
	progressLine := makeProgressEntry(t, "sess-1", "2026-03-25T10:00:02.000Z", "toolu_agent1", "abc123")

	// Tool result completing the agent.
	resultLine := makeToolResultEntry(t, "sess-1", "2026-03-25T10:00:10.000Z", "toolu_agent1", map[string]any{
		"status":          "completed",
		"agentId":         "abc123",
		"totalDurationMs": 10000,
	})

	data := joinLines(agentLine, progressLine, resultLine)
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)

	require.Len(t, state.Subagents, 1)
	sa := state.Subagents[0]
	assert.Equal(t, "toolu_agent1", sa.ToolUseID)
	assert.Equal(t, "Research codebase", sa.Description)
	assert.Equal(t, "Explore", sa.SubagentType)
	assert.Equal(t, "abc123", sa.AgentID)
	assert.NotNil(t, sa.CompletedAt)
	assert.Equal(t, int64(10000), sa.DurationMs)
}

func TestFileParser_TaskLifecycle(t *testing.T) {
	p := NewFileParser()

	// TaskCreate tool_use.
	toolUse := new("tool_use")
	createLine := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type": "tool_use",
			"id":   "toolu_task1",
			"name": "TaskCreate",
			"input": map[string]any{
				"subject":     "Fix the bug",
				"description": "There is a bug to fix",
			},
		},
	})

	// TaskCreate result with task ID.
	createResult := makeToolResultEntry(t, "sess-1", "2026-03-25T10:00:01.000Z", "toolu_task1", map[string]any{
		"task": map[string]any{
			"id":      "1",
			"subject": "Fix the bug",
		},
	})

	// TaskUpdate to in_progress.
	updateLine := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:05.000Z", toolUse, []map[string]any{
		{
			"type": "tool_use",
			"id":   "toolu_update1",
			"name": "TaskUpdate",
			"input": map[string]any{
				"taskId": "1",
				"status": "in_progress",
			},
		},
	})

	// TaskUpdate to completed.
	completeLine := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:20.000Z", toolUse, []map[string]any{
		{
			"type": "tool_use",
			"id":   "toolu_update2",
			"name": "TaskUpdate",
			"input": map[string]any{
				"taskId": "1",
				"status": "completed",
			},
		},
	})

	data := joinLines(createLine, createResult, updateLine, completeLine)
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)

	require.Len(t, state.Tasks, 1)
	task := state.Tasks[0]
	assert.Equal(t, "1", task.TaskID)
	assert.Equal(t, "Fix the bug", task.Subject)
	assert.Equal(t, "completed", task.Status)
}

func TestFileParser_StatusTransitions(t *testing.T) {
	p := NewFileParser()

	// User prompt → working.
	line1 := makeUserEntry(t, "sess-1", "/project", "", "do stuff", "2026-03-25T10:00:00.000Z")
	data1 := joinLines(line1)
	state, _ := p.UpdateBytes(data1)
	assert.Equal(t, StatusWorking, state.Status, "user prompt should set IsWorking")

	// Assistant with tool_use stop_reason → still working.
	toolUse := new("tool_use")
	line2 := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:02.000Z", toolUse, []map[string]any{
		{"type": "text", "text": "let me check"},
	})
	data2 := joinLines(line2)
	state, _ = p.UpdateBytes(data2)
	assert.Equal(t, StatusWorking, state.Status, "tool_use stop_reason should keep IsWorking")

	// Assistant with end_turn → idle.
	endTurn := new("end_turn")
	line3 := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:10.000Z", endTurn, []map[string]any{
		{"type": "text", "text": "all done"},
	})
	data3 := joinLines(line3)
	state, _ = p.UpdateBytes(data3)
	assert.Equal(t, StatusReady, state.Status, "end_turn should set StatusReady")

	// New user prompt → working again.
	line4 := makeUserEntry(t, "sess-1", "/project", "", "more work", "2026-03-25T10:01:00.000Z")
	data4 := joinLines(line4)
	state, _ = p.UpdateBytes(data4)
	assert.Equal(t, StatusWorking, state.Status, "new user prompt should set IsWorking again")
}

func TestFileParser_ToolResultSetsWorking(t *testing.T) {
	p := NewFileParser()

	// AskUserQuestion tool_use → StatusWaiting.
	toolUse := new("tool_use")
	askLine := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type":  "tool_use",
			"id":    "toolu_ask1",
			"name":  "AskUserQuestion",
			"input": map[string]any{"question": "which approach?"},
		},
	})
	data := joinLines(askLine)
	state, _ := p.UpdateBytes(data)
	assert.Equal(t, StatusWaiting, state.Status, "AskUserQuestion should set StatusWaiting")

	// User answers with tool_result → StatusWorking.
	answerLine := makeToolResultEntry(t, "sess-1", "2026-03-25T10:00:10.000Z", "toolu_ask1", nil)
	data2 := joinLines(answerLine)
	state, _ = p.UpdateBytes(data2)
	assert.Equal(t, StatusWorking, state.Status, "tool_result should transition to StatusWorking")
}

func TestFileParser_MultipleSubagents(t *testing.T) {
	p := NewFileParser()

	toolUse := new("tool_use")

	// Two agents launched in same assistant turn.
	agentLine := makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type": "tool_use", "id": "toolu_a1", "name": "Agent",
			"input": map[string]any{"description": "Agent One", "subagent_type": "Explore"},
		},
		{
			"type": "tool_use", "id": "toolu_a2", "name": "Agent",
			"input": map[string]any{"description": "Agent Two", "subagent_type": "Plan"},
		},
	})

	// Only first completes.
	result1 := makeToolResultEntry(t, "sess-1", "2026-03-25T10:00:05.000Z", "toolu_a1", map[string]any{
		"status":          "completed",
		"agentId":         "id1",
		"totalDurationMs": 5000,
	})

	data := joinLines(agentLine, result1)
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)

	require.Len(t, state.Subagents, 2)

	// First agent completed.
	assert.Equal(t, "Agent One", state.Subagents[0].Description)
	assert.NotNil(t, state.Subagents[0].CompletedAt)

	// Second agent still running.
	assert.Equal(t, "Agent Two", state.Subagents[1].Description)
	assert.Nil(t, state.Subagents[1].CompletedAt)
}

func TestFileParser_MalformedLines(t *testing.T) {
	p := NewFileParser()

	data := joinLines(
		[]byte(`{invalid json`),
		[]byte(``),
		makeUserEntry(t, "sess-1", "/project", "", "hello", "2026-03-25T10:00:00.000Z"),
		[]byte(`{"type": "unknown_type"}`),
	)

	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", state.SessionID) // valid line was processed
}

func TestFileParser_PartialLine(t *testing.T) {
	p := NewFileParser()

	completeLine := string(makeUserEntry(t, "sess-1", "/project", "", "hi", "2026-03-25T10:00:00.000Z"))
	partial := `{"type": "assistant", "sessionId": "sess-1"`

	// Data with complete line + partial line (no trailing newline).
	data := []byte(completeLine + "\n" + partial)

	consumed := p.parseBytes(data)

	// Should only consume the complete line (including its newline).
	assert.Equal(t, len(completeLine)+1, consumed)
	assert.Equal(t, "sess-1", p.state.SessionID)
}

func TestFileParser_Update_WithFileReader(t *testing.T) {
	p := NewFileParser()

	data := joinLines(
		makeUserEntry(t, "sess-1", "/project", "my-slug", "hello", "2026-03-25T10:00:00.000Z"),
	)

	reader := &memoryReader{data: data}
	state, err := p.Update(reader, "/fake/path")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", state.SessionID)
	assert.Equal(t, "my-slug", state.Slug)
}

// memoryReader implements FileReader for testing.
type memoryReader struct {
	data []byte
}

func (r *memoryReader) ReadFrom(_ string, offset int64) ([]byte, int64, error) {
	if offset >= int64(len(r.data)) {
		return nil, offset, nil
	}
	d := r.data[offset:]
	return d, int64(len(r.data)), nil
}

func TestFileParser_State(t *testing.T) {
	p := NewFileParser()
	data := joinLines(
		makeUserEntry(t, "sess-1", "/project", "my-slug", "hello", "2026-03-25T10:00:00.000Z"),
	)
	_, _ = p.UpdateBytes(data)

	state := p.State()
	assert.Equal(t, "sess-1", state.SessionID)
	assert.Equal(t, "/project", state.Cwd)
	assert.Equal(t, "my-slug", state.Slug)
	assert.Equal(t, StatusWorking, state.Status)
}

func TestFileParser_AssistantNoMessage(t *testing.T) {
	p := NewFileParser()
	// Assistant entry with no message field at all should not crash.
	data := joinLines(marshalLine(t, map[string]any{
		"type":      "assistant",
		"sessionId": "sess-1",
		"timestamp": "2026-03-25T10:00:00.000Z",
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", state.SessionID)
}

func TestFileParser_AssistantNoStopReason(t *testing.T) {
	p := NewFileParser()
	// Assistant with message but no stop_reason means it's streaming (working).
	data := joinLines(makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", nil, []map[string]any{
		{"type": "text", "text": "thinking..."},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, StatusWorking, state.Status)
}

func TestFileParser_ProgressNonAgentIgnored(t *testing.T) {
	p := NewFileParser()
	// Progress entry with a non-agent_progress type should be ignored.
	data := joinLines(marshalLine(t, map[string]any{
		"type":      "progress",
		"sessionId": "sess-1",
		"timestamp": "2026-03-25T10:00:00.000Z",
		"data": map[string]any{
			"type": "other_progress",
		},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Subagents)
}

func TestFileParser_ProgressWithoutActiveSubagent(t *testing.T) {
	p := NewFileParser()
	// Progress entry for a non-existent subagent toolUseID should not crash.
	data := joinLines(makeProgressEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", "nonexistent-id", "agent-xyz"))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Subagents)
}

func TestFileParser_UserMessageNoContent(t *testing.T) {
	p := NewFileParser()
	// User entry with nil message should not crash.
	data := joinLines(marshalLine(t, map[string]any{
		"type":      "user",
		"sessionId": "sess-1",
		"timestamp": "2026-03-25T10:00:00.000Z",
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", state.SessionID)
}

func TestFileParser_TaskUpdateUnknownTaskID(t *testing.T) {
	p := NewFileParser()
	toolUse := new("tool_use")
	// TaskUpdate referencing a task that was never created.
	data := joinLines(makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type": "tool_use",
			"id":   "toolu_update_unknown",
			"name": "TaskUpdate",
			"input": map[string]any{
				"taskId": "nonexistent-task",
				"status": "completed",
			},
		},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Tasks) // no task was created, update should be a no-op
}

func TestFileParser_ToolResultForUnknownToolUse(t *testing.T) {
	p := NewFileParser()
	// Tool result for a toolUseID that's not tracked as agent or task.
	data := joinLines(makeToolResultEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", "unknown-tool-use", nil))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Subagents)
	assert.Empty(t, state.Tasks)
}

func TestFileParser_ExitPlanModeIsWaiting(t *testing.T) {
	p := NewFileParser()
	toolUse := new("tool_use")
	// ExitPlanMode is a user-blocking tool, should set StatusWaiting.
	data := joinLines(makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type":  "tool_use",
			"id":    "toolu_exit",
			"name":  "ExitPlanMode",
			"input": map[string]any{},
		},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, StatusWaiting, state.Status)
}

func TestFileParser_MixedBlockingAndNonBlockingTools(t *testing.T) {
	p := NewFileParser()
	toolUse := new("tool_use")
	// When both a blocking tool and a non-blocking tool are present,
	// status should be Working (not Waiting).
	data := joinLines(makeAssistantEntry(t, "sess-1", "2026-03-25T10:00:00.000Z", toolUse, []map[string]any{
		{
			"type":  "tool_use",
			"id":    "toolu_ask",
			"name":  "AskUserQuestion",
			"input": map[string]any{"question": "which?"},
		},
		{
			"type":  "tool_use",
			"id":    "toolu_read",
			"name":  "Read",
			"input": map[string]any{"path": "/tmp/foo"},
		},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Equal(t, StatusWorking, state.Status)
}

func TestFileParser_OversizedLine(t *testing.T) {
	p := NewFileParser()
	// Create a valid line followed by an oversized line (>1 MiB).
	validLine := makeUserEntry(t, "sess-1", "/project", "", "hello", "2026-03-25T10:00:00.000Z")
	// Build a line that exceeds the scanner buffer (1 MiB).
	oversized := make([]byte, 2*1024*1024)
	for i := range oversized {
		oversized[i] = 'A'
	}
	data := append(validLine, '\n')
	data = append(data, oversized...)
	data = append(data, '\n')

	consumed := p.parseBytes(data)
	// Should consume past the oversized line (all data).
	assert.Equal(t, len(data), consumed)
	assert.Equal(t, "sess-1", p.state.SessionID) // valid line was processed
}

func TestFileParser_BadAgentInput(t *testing.T) {
	p := NewFileParser()
	toolUse := new("tool_use")
	// Agent tool_use with invalid JSON input.
	data := joinLines(marshalLine(t, map[string]any{
		"type":      "assistant",
		"sessionId": "sess-1",
		"timestamp": "2026-03-25T10:00:00.000Z",
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"id":    "toolu_bad_agent",
					"name":  "Agent",
					"input": "not-a-json-object", // invalid
				},
			},
		},
	}))
	_ = toolUse
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Subagents) // should not crash, just skip
}

func TestFileParser_BadTaskCreateInput(t *testing.T) {
	p := NewFileParser()
	data := joinLines(marshalLine(t, map[string]any{
		"type":      "assistant",
		"sessionId": "sess-1",
		"timestamp": "2026-03-25T10:00:00.000Z",
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"id":    "toolu_bad_task",
					"name":  "TaskCreate",
					"input": "not-a-json-object",
				},
			},
		},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Tasks)
}

func TestFileParser_BadTaskUpdateInput(t *testing.T) {
	p := NewFileParser()
	data := joinLines(marshalLine(t, map[string]any{
		"type":      "assistant",
		"sessionId": "sess-1",
		"timestamp": "2026-03-25T10:00:00.000Z",
		"message": map[string]any{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []map[string]any{
				{
					"type":  "tool_use",
					"id":    "toolu_bad_update",
					"name":  "TaskUpdate",
					"input": "not-a-json-object",
				},
			},
		},
	}))
	state, err := p.UpdateBytes(data)
	require.NoError(t, err)
	assert.Empty(t, state.Tasks)
}
