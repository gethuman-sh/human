package claude

import (
	"strings"
	"testing"
)

// Every agent/skill that resolves a dispatched ticket key must route the key
// through the CLI's auto-detection (`human get`), never guess the owning
// tracker by hand or infer it from the git remote (Shortcut story 876).
func TestEmbeddedAgentsResolveKeysViaAutoDetect(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{"planner", agentContent},
		{"done", doneAgentContent},
		{"executor", executorAgentContent},
		{"reviewer", reviewerAgentContent},
		{"ideator", ideatorAgentContent},
		{"ready", readyAgentContent},
		{"bugAnalyzer", bugAnalyzerAgentContent},
		{"bugTriage", bugTriageAgentContent},
		{"autofix", autofixSkillContent},
	}

	// Phrasing that forces the agent to guess a tracker instead of letting the
	// CLI auto-detect it from the key shape.
	forbidden := []string{
		"use provider-specific commands",
		"to find the right tracker",
	}
	// Canonical phrase asserting auto-detection is unconditional.
	const required = "regardless of how many trackers are configured"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := string(tc.content)
			for _, f := range forbidden {
				if strings.Contains(body, f) {
					t.Errorf("%s: must not instruct guessing a tracker; found %q", tc.name, f)
				}
			}
			if !strings.Contains(body, required) {
				t.Errorf("%s: must state that `human get` auto-detection works %q", tc.name, required)
			}
			if !strings.Contains(body, "human get") {
				t.Errorf("%s: must resolve dispatched keys via `human get`", tc.name)
			}
		})
	}
}
