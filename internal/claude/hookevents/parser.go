package hookevents

import (
	"bufio"
	"bytes"
	"encoding/json"

	"github.com/gethuman-sh/human/internal/claude/logparser"
)

// Parse reads all event lines and returns the latest snapshot per session.
// The file is max 100 lines (trimmed by the hook script), so this is cheap.
func Parse(data []byte) map[string]SessionSnapshot {
	sessions := make(map[string]SessionSnapshot)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt Event
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.SessionID == "" {
			continue
		}
		snap := sessions[evt.SessionID]
		snap.SessionID = evt.SessionID
		if evt.Cwd != "" {
			snap.Cwd = evt.Cwd
		}
		snap.LastEventAt = evt.Timestamp
		ApplyEvent(&snap, &evt)
		sessions[evt.SessionID] = snap
	}
	return sessions
}

// ApplyEvent updates a session snapshot based on a hook event.
func ApplyEvent(snap *SessionSnapshot, evt *Event) {
	if snap == nil || evt == nil {
		return
	}
	switch evt.EventName {
	case "UserPromptSubmit", "SubagentStart":
		snap.Status = logparser.StatusWorking
		snap.ErrorType = ""
		snap.CurrentTool = ""
		snap.BlockedTool = ""

	case "PreToolUse":
		if isUserBlockingTool(evt.ToolName) {
			snap.Status = logparser.StatusWaiting
			snap.CurrentTool = ""
		} else {
			snap.CurrentTool = evt.ToolName
		}

	case "PostToolUse", "PostToolUseFailure":
		snap.CurrentTool = ""
		snap.BlockedTool = ""
		snap.Status = logparser.StatusWorking

	case "Stop", "SubagentStop":
		// Stop after StopFailure keeps error visible.
		if snap.Status != logparser.StatusError {
			snap.Status = logparser.StatusReady
		}
		snap.CurrentTool = ""
		snap.BlockedTool = ""

	case "StopFailure":
		snap.Status = logparser.StatusError
		snap.ErrorType = evt.ErrorType
		snap.CurrentTool = ""

	case "PermissionRequest":
		snap.Status = logparser.StatusBlocked
		snap.BlockedTool = evt.ToolName
		snap.CurrentTool = ""

	case "Notification":
		applyNotification(snap, evt)

	case "SessionStart":
		snap.Status = logparser.StatusReady
		snap.CurrentTool = ""
		snap.BlockedTool = ""
		snap.ErrorType = ""

	case "SessionEnd":
		snap.Status = logparser.StatusEnded
		snap.CurrentTool = ""
		snap.BlockedTool = ""
	}
}

func applyNotification(snap *SessionSnapshot, evt *Event) {
	switch evt.NotificationType {
	case "idle_prompt":
		snap.Status = logparser.StatusReady
		snap.CurrentTool = ""
		snap.BlockedTool = ""
	case "permission_prompt":
		snap.Status = logparser.StatusBlocked
	}
}

// isUserBlockingTool returns true for tools that block on user input.
// Keep in sync with logparser.isUserBlockingToolUse().
func isUserBlockingTool(name string) bool {
	switch name {
	case "AskUserQuestion", "ExitPlanMode":
		return true
	default:
		return false
	}
}
