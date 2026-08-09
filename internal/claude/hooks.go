package claude

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/gethuman-sh/human/errors"
)

const hookCommand = "human hook"

// agentContextHookCommand primes each new session with the guidance from
// `human agent-context` via the SessionStart hook's additionalContext output.
// It runs in addition to the monitoring `human hook` on SessionStart.
const agentContextHookCommand = "human agent-context --hook"

// hookEvents lists the Claude Code hook events we register for.
var hookEvents = []struct {
	name    string
	async   bool
	matcher string // "" for default empty matcher; set for events like Notification
}{
	{"UserPromptSubmit", false, ""}, // blocking — must not be async
	{"Stop", true, ""},
	{"SubagentStart", true, ""},
	{"SubagentStop", true, ""},
	{"PreToolUse", true, ""},         // tool about to execute — current activity indicator
	{"PostToolUse", true, ""},        // tool completed — transitions waiting/blocked → working
	{"PostToolUseFailure", true, ""}, // tool failed
	{"PermissionRequest", true, ""},  // blocked waiting for tool permission
	{"Notification", true, ".*"},     // catches idle_prompt, permission_prompt, etc.
	{"StopFailure", true, ""},        // API error or crash
	{"SessionStart", true, ""},       // new session began
	{"SessionEnd", true, ""},         // session ended (e.g. /clear)
}

// InstallHooks registers hooks in ~/.claude/settings.json.
// The hooks invoke `human hook` directly — no script file needed.
func InstallHooks(w io.Writer, fw FileWriter) error {
	home, err := userHomeDir()
	if err != nil {
		return errors.WrapWithDetails(err, "resolving home directory")
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := mergeHooksIntoSettings(w, fw, settingsPath); err != nil {
		return err
	}

	return nil
}

func mergeHooksIntoSettings(w io.Writer, fw FileWriter, path string) error {
	settings, err := LoadSettings(fw, path)
	if err != nil {
		return err
	}

	for _, evt := range hookEvents {
		if err := settings.AddHook(evt.name, hookCommand, evt.async, evt.matcher); err != nil {
			return err
		}
	}
	// SessionStart also injects the agent-context guidance. It runs synchronously
	// (not async) so Claude Code reads its additionalContext output as context.
	if err := settings.AddHook("SessionStart", agentContextHookCommand, false, ""); err != nil {
		return err
	}

	if !settings.Changed() {
		_, _ = fmt.Fprintf(w, "  unchanged %s (hooks already registered)\n", path)
		return nil
	}
	if err := settings.Save(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "  updated %s (hooks registered)\n", path)
	return nil
}
