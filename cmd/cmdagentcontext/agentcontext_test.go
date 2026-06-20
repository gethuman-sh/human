package cmdagentcontext

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentContext_PlainOutput(t *testing.T) {
	cmd := BuildAgentContextCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"human codenav", "human get <KEY>", "human pr create"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain output is missing %q", want)
		}
	}
}

// TestAgentContext_HookJSON locks the shape the SessionStart hook relies on:
// hookSpecificOutput.{hookEventName,additionalContext}.
func TestAgentContext_HookJSON(t *testing.T) {
	cmd := BuildAgentContextCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--hook"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("--hook output is not valid JSON: %v", err)
	}
	if out.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", out.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, "human codenav") {
		t.Error("additionalContext is missing the codenav guidance")
	}
}
