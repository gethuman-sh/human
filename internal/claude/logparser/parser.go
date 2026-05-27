package logparser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// FileParser incrementally parses a JSONL session file, tracking state across calls.
type FileParser struct {
	offset          int64
	state           SessionState
	activeSubagents map[string]*Subagent
	tasksByToolUse  map[string]*Task
	tasksByID       map[string]*Task
}

// NewFileParser creates a new parser with empty state.
func NewFileParser() *FileParser {
	return &FileParser{
		activeSubagents: make(map[string]*Subagent),
		tasksByToolUse:  make(map[string]*Task),
		tasksByID:       make(map[string]*Task),
	}
}

// Update reads new data from the file and updates internal state.
func (p *FileParser) Update(reader FileReader, path string) (SessionState, error) {
	data, newOffset, err := reader.ReadFrom(path, p.offset)
	if err != nil {
		return p.state, errors.WrapWithDetails(err, "reading session file", "path", path)
	}
	if len(data) == 0 {
		return p.state, nil
	}

	consumed := p.parseBytes(data)
	p.offset = p.offset + int64(consumed)
	_ = newOffset // we compute our own offset based on consumed bytes
	return p.state, nil
}

// UpdateBytes parses raw JSONL bytes (for testing). Resets offset to 0 on first call.
func (p *FileParser) UpdateBytes(data []byte) (SessionState, error) {
	p.parseBytes(data)
	return p.state, nil
}

// State returns the current accumulated state.
func (p *FileParser) State() SessionState {
	return p.state
}

// parseBytes processes JSONL data line by line, returning the number of bytes consumed.
// Incomplete trailing lines (no newline) are not consumed.
func (p *FileParser) parseBytes(data []byte) int {
	consumed := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 512*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		consumed += len(line) + 1 // +1 for newline

		if len(line) == 0 {
			continue
		}
		p.processLine(line)
	}

	// Claude PostToolUse lines containing screenshots or AX trees can
	// exceed 1 MiB. Without forward progress the same line is retried
	// every poll and the session is permanently stuck — skip past the
	// next newline so subsequent parses see fresh data.
	if err := scanner.Err(); err == bufio.ErrTooLong {
		skipStart := consumed
		if skipStart > len(data) {
			skipStart = len(data)
		}
		if nextNL := bytes.IndexByte(data[skipStart:], '\n'); nextNL >= 0 {
			consumed = skipStart + nextNL + 1
		} else {
			consumed = len(data)
		}
		return consumed
	}

	// If data doesn't end with newline, there may be a partial line.
	// bufio.Scanner consumes it, but we should only count up to the last newline.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		// Find the last newline and only consume up to there.
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL >= 0 {
			consumed = lastNL + 1
		} else {
			consumed = 0 // no complete lines at all
		}
	}

	return consumed
}

// jsonlEntry is the minimal structure for parsing JSONL lines.
type jsonlEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Slug      string `json:"slug"`
	Message   *struct {
		StopReason *string `json:"stop_reason"`
		Content    []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name,omitempty"`
			ID        string          `json:"id,omitempty"`
			ToolUseID string          `json:"tool_use_id,omitempty"`
			Input     json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
	} `json:"message,omitempty"`
	ToolUseResult json.RawMessage `json:"toolUseResult,omitempty"`
	Data          *struct {
		Type    string `json:"type"`
		AgentID string `json:"agentId,omitempty"`
	} `json:"data,omitempty"`
	ParentToolUseID string `json:"parentToolUseID,omitempty"`
}

type agentInput struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
}

type taskCreateInput struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
}

type taskUpdateInput struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

type agentResult struct {
	Status          string `json:"status"`
	AgentID         string `json:"agentId"`
	TotalDurationMs int64  `json:"totalDurationMs"`
}

type taskCreateResult struct {
	Task struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	} `json:"task"`
}

func (p *FileParser) processLine(line []byte) {
	var entry jsonlEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return // skip malformed
	}

	ts := parseTimestamp(entry.Timestamp)
	if !ts.IsZero() {
		p.state.LastActivity = ts
	}

	// Track session identity. When a new session starts in the same file
	// (different sessionId), reset all accumulated state so stale tasks,
	// subagents, and metadata from the previous session don't leak through.
	if entry.SessionID != "" {
		if p.state.SessionID == "" {
			p.state.SessionID = entry.SessionID
		} else if entry.SessionID != p.state.SessionID {
			p.state = SessionState{
				SessionID:    entry.SessionID,
				Cwd:          entry.Cwd,
				Slug:         entry.Slug,
				StartedAt:    ts,
				LastActivity: ts,
			}
			p.tasksByID = make(map[string]*Task)
			p.tasksByToolUse = make(map[string]*Task)
			p.activeSubagents = make(map[string]*Subagent)
			return // metadata already set from this entry
		}
	}
	if p.state.Cwd == "" && entry.Cwd != "" {
		p.state.Cwd = entry.Cwd
	}
	if p.state.Slug == "" && entry.Slug != "" {
		p.state.Slug = entry.Slug
	}
	if p.state.StartedAt.IsZero() && !ts.IsZero() {
		p.state.StartedAt = ts
	}

	switch entry.Type {
	case "assistant":
		p.processAssistant(&entry, ts)
	case "user":
		p.processUser(&entry, ts)
	case "progress":
		p.processProgress(&entry)
	}
}

func (p *FileParser) processAssistant(entry *jsonlEntry, ts time.Time) {
	if entry.Message == nil {
		return
	}

	// Status detection from stop_reason.
	if entry.Message.StopReason == nil {
		p.state.Status = StatusWorking
	} else {
		switch *entry.Message.StopReason {
		case "end_turn":
			p.state.Status = StatusReady
		default: // "tool_use" etc.
			if isUserBlockingToolUse(entry) {
				p.state.Status = StatusWaiting
			} else {
				p.state.Status = StatusWorking
			}
		}
	}

	// Check for tool_use entries.
	for i := range entry.Message.Content {
		c := &entry.Message.Content[i]
		if c.Type != "tool_use" {
			continue
		}
		switch c.Name {
		case "Agent":
			p.handleAgentToolUse(c.ID, c.Input, ts)
		case "TaskCreate":
			p.handleTaskCreate(c.ID, c.Input, ts)
		case "TaskUpdate":
			p.handleTaskUpdate(c.Input, ts)
		}
	}
}

// isUserBlockingToolUse returns true when the assistant message contains only
// tools that block on user input (e.g. AskUserQuestion, ExitPlanMode).
func isUserBlockingToolUse(entry *jsonlEntry) bool {
	if entry.Message == nil {
		return false
	}
	hasToolUse := false
	for i := range entry.Message.Content {
		c := &entry.Message.Content[i]
		if c.Type != "tool_use" {
			continue
		}
		hasToolUse = true
		switch c.Name {
		case "AskUserQuestion", "ExitPlanMode":
			// user-blocking — continue checking
		default:
			return false // non-blocking tool present
		}
	}
	return hasToolUse
}

func (p *FileParser) processUser(entry *jsonlEntry, ts time.Time) {
	if entry.Message == nil {
		return
	}

	for i := range entry.Message.Content {
		c := &entry.Message.Content[i]
		if c.Type == "tool_result" && c.ToolUseID != "" {
			p.handleToolResult(c.ToolUseID, entry.ToolUseResult)
		}
	}

	// Any user message (text prompt or tool_result) means Claude is about to work.
	// This is critical for AskUserQuestion/ExitPlanMode: without this, the status
	// stays StatusWaiting after the user answers because tool_results were excluded.
	p.state.Status = StatusWorking
}

func (p *FileParser) processProgress(entry *jsonlEntry) {
	if entry.Data == nil || entry.Data.Type != "agent_progress" {
		return
	}
	// Update agentID on active subagent if we can match via parentToolUseID.
	if sa, ok := p.activeSubagents[entry.ParentToolUseID]; ok {
		if entry.Data.AgentID != "" {
			sa.AgentID = entry.Data.AgentID
		}
	}
}

func (p *FileParser) handleAgentToolUse(toolUseID string, input json.RawMessage, ts time.Time) {
	var ai agentInput
	if err := json.Unmarshal(input, &ai); err != nil {
		return
	}
	sa := &Subagent{
		ToolUseID:    toolUseID,
		Description:  ai.Description,
		SubagentType: ai.SubagentType,
		StartedAt:    ts,
	}
	p.activeSubagents[toolUseID] = sa
	p.state.Subagents = append(p.state.Subagents, *sa)
}

func (p *FileParser) handleTaskCreate(toolUseID string, input json.RawMessage, ts time.Time) {
	var ti taskCreateInput
	if err := json.Unmarshal(input, &ti); err != nil {
		return
	}
	task := &Task{
		ToolUseID: toolUseID,
		Subject:   ti.Subject,
		Status:    "pending",
		CreatedAt: ts,
		UpdatedAt: ts,
	}
	p.tasksByToolUse[toolUseID] = task
	p.state.Tasks = append(p.state.Tasks, *task)
}

func (p *FileParser) handleTaskUpdate(input json.RawMessage, ts time.Time) {
	var tu taskUpdateInput
	if err := json.Unmarshal(input, &tu); err != nil {
		return
	}
	if task, ok := p.tasksByID[tu.TaskID]; ok {
		task.Status = tu.Status
		task.UpdatedAt = ts
		p.updateTaskInSlice(task)
	}
}

func (p *FileParser) handleToolResult(toolUseID string, rawResult json.RawMessage) {
	// Check if this completes a subagent.
	if sa, ok := p.activeSubagents[toolUseID]; ok {
		now := time.Now()
		sa.CompletedAt = &now

		if len(rawResult) > 0 {
			var ar agentResult
			if err := json.Unmarshal(rawResult, &ar); err == nil {
				if ar.AgentID != "" {
					sa.AgentID = ar.AgentID
				}
				if ar.TotalDurationMs > 0 {
					sa.DurationMs = ar.TotalDurationMs
					completed := sa.StartedAt.Add(time.Duration(ar.TotalDurationMs) * time.Millisecond)
					sa.CompletedAt = &completed
				}
			}
		}

		p.updateSubagentInSlice(sa)
		delete(p.activeSubagents, toolUseID)
		return
	}

	// Check if this is a TaskCreate result that gives us the taskId.
	if task, ok := p.tasksByToolUse[toolUseID]; ok {
		if len(rawResult) > 0 {
			var tcr taskCreateResult
			if err := json.Unmarshal(rawResult, &tcr); err == nil && tcr.Task.ID != "" {
				task.TaskID = tcr.Task.ID
				p.tasksByID[tcr.Task.ID] = task
				p.updateTaskInSlice(task)
			}
		}
		return
	}
}

func (p *FileParser) updateSubagentInSlice(sa *Subagent) {
	for i := range p.state.Subagents {
		if p.state.Subagents[i].ToolUseID == sa.ToolUseID {
			p.state.Subagents[i] = *sa
			return
		}
	}
}

func (p *FileParser) updateTaskInSlice(task *Task) {
	for i := range p.state.Tasks {
		if p.state.Tasks[i].ToolUseID == task.ToolUseID {
			p.state.Tasks[i] = *task
			return
		}
	}
}

func parseTimestamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// Try alternate formats.
		t, err = time.Parse("2006-01-02T15:04:05.000Z", s)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}
