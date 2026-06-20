package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	settings := make(map[string]interface{})

	data, err := fw.ReadFile(path)
	if err == nil {
		if jsonErr := json.Unmarshal(data, &settings); jsonErr != nil {
			return errors.WrapWithDetails(jsonErr, "parsing settings.json", "path", path)
		}
	} else if !os.IsNotExist(err) {
		// Anything other than "not found" — permission denied, NFS stall,
		// I/O error — must propagate so we never overwrite a settings file
		// the user can't currently read with a fresh empty one.
		return errors.WrapWithDetails(err, "reading settings.json", "path", path)
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	changed := false
	for _, evt := range hookEvents {
		if addHookCommand(hooks, evt.name, hookCommand, evt.async, evt.matcher) {
			changed = true
		}
	}
	// SessionStart also injects the agent-context guidance. It runs synchronously
	// (not async) so Claude Code reads its additionalContext output as context.
	if addHookCommand(hooks, "SessionStart", agentContextHookCommand, false, "") {
		changed = true
	}

	if !changed {
		_, _ = fmt.Fprintf(w, "  unchanged %s (hooks already registered)\n", path)
		return nil
	}

	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling settings.json")
	}
	out = append(out, '\n')

	if err := fw.WriteFile(path, out, 0o644); err != nil {
		return errors.WrapWithDetails(err, "writing settings.json", "path", path)
	}

	_, _ = fmt.Fprintf(w, "  updated %s (hooks registered)\n", path)
	return nil
}

// addHookCommand registers a command for an event if that exact command is not
// already present. Returns true if a new matcher was added.
func addHookCommand(hooks map[string]interface{}, eventName, command string, async bool, matcher string) bool {
	matchers, _ := hooks[eventName].([]interface{})

	// Check if this command is already registered for the event.
	for _, m := range matchers {
		matcherObj, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		hookList, _ := matcherObj["hooks"].([]interface{})
		for _, h := range hookList {
			hookDef, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if cmd, _ := hookDef["command"].(string); cmd == command {
				return false // already registered
			}
		}
	}

	hookDef := map[string]interface{}{
		"type":    "command",
		"command": command,
	}
	if async {
		hookDef["async"] = true
	}

	newMatcher := map[string]interface{}{
		"matcher": matcher,
		"hooks":   []interface{}{hookDef},
	}
	hooks[eventName] = append(matchers, newMatcher)
	return true
}
