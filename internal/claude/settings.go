package claude

import (
	"encoding/json"
	"os"

	"github.com/gethuman-sh/human/errors"
)

// Settings is one Claude Code settings.json — the user's file, which `human`
// edits but does not own.
//
// Three places used to edit it, each with its own copy of load, merge and
// write: the hook installer, the bot git identity, and the LSP enablement. Each
// reached into the decoded JSON with a type assertion whose failure is silent —
// `settings["env"].(map[string]any)` yields nil for a key holding anything
// else, the code then built a fresh map, and the write REPLACED what was there.
// A user whose env or hooks block was any other shape lost it, with nothing
// said. That is the failure this type exists to make unwritable: a key holding
// the wrong shape is an error the caller must see, never a key to overwrite.
//
// Everything else about the file is preserved byte-for-byte-ish: unknown keys
// round-trip untouched, and a file that needs no change is not rewritten at all.
type Settings struct {
	fw     FileWriter
	path   string
	values map[string]any
	// changed records whether a mutator actually altered anything, so a caller
	// can report "unchanged" and skip the write — the settings file of a user
	// who runs `human install` twice must not be rewritten the second time.
	changed bool
}

// LoadSettings reads path, or starts an empty document when it does not exist.
//
// Any read error other than "not found" is returned rather than swallowed: a
// settings file that is momentarily unreadable — permission denied, an NFS
// stall — must never be replaced with a fresh empty one.
func LoadSettings(fw FileWriter, path string) (*Settings, error) {
	s := &Settings{fw: fw, path: path, values: map[string]any{}}

	data, err := fw.ReadFile(path)
	switch {
	case err == nil:
		if jsonErr := json.Unmarshal(data, &s.values); jsonErr != nil {
			return nil, errors.WrapWithDetails(jsonErr, "parsing settings.json", "path", path)
		}
		if s.values == nil {
			s.values = map[string]any{}
		}
	case os.IsNotExist(err):
	default:
		return nil, errors.WrapWithDetails(err, "reading settings.json", "path", path)
	}
	return s, nil
}

// Path is the file this document came from, for the messages a caller prints.
func (s *Settings) Path() string { return s.path }

// Changed reports whether a mutator altered the document.
func (s *Settings) Changed() bool { return s.changed }

// object returns the object under key, creating it when the key is absent.
//
// A key holding a non-object is refused. Silently treating it as absent is what
// turned "your hooks are shaped oddly" into "your hooks are gone".
func (s *Settings) object(key string) (map[string]any, error) {
	raw, present := s.values[key]
	if !present || raw == nil {
		created := map[string]any{}
		s.values[key] = created
		return created, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.WithDetails("settings key does not hold an object",
			"path", s.path, "key", key,
			"hint", "fix or remove the key by hand — refusing to overwrite it")
	}
	return obj, nil
}

// SetEnv sets an environment variable Claude Code injects into the session.
func (s *Settings) SetEnv(key, value string) error {
	env, err := s.object("env")
	if err != nil {
		return err
	}
	if current, _ := env[key].(string); current == value {
		return nil
	}
	env[key] = value
	s.changed = true
	return nil
}

// EnvValue reads an environment variable back, and whether it is set to a
// string at all.
func (s *Settings) EnvValue(key string) (string, bool) {
	env, ok := s.values["env"].(map[string]any)
	if !ok {
		return "", false
	}
	value, ok := env[key].(string)
	return value, ok
}

// AddHook registers command for a hook event unless that exact command is
// already registered for it, so installing twice adds nothing.
//
// matcher is the event's matcher pattern ("" for the default). async marks a
// hook Claude Code may run without waiting; a blocking hook must not set it.
func (s *Settings) AddHook(event, command string, async bool, matcher string) error {
	hooks, err := s.object("hooks")
	if err != nil {
		return err
	}
	matchers, err := hookMatchers(s.path, hooks, event)
	if err != nil {
		return err
	}
	if hookCommandRegistered(matchers, command) {
		return nil
	}

	hookDef := map[string]any{"type": "command", "command": command}
	if async {
		hookDef["async"] = true
	}
	hooks[event] = append(matchers, map[string]any{
		"matcher": matcher,
		"hooks":   []any{hookDef},
	})
	s.changed = true
	return nil
}

// hookMatchers reads an event's matcher list. Like object(), a value of the
// wrong shape is refused rather than replaced — appending to a fresh list here
// would drop whatever the user had registered for that event.
func hookMatchers(path string, hooks map[string]any, event string) ([]any, error) {
	raw, present := hooks[event]
	if !present || raw == nil {
		return nil, nil
	}
	matchers, ok := raw.([]any)
	if !ok {
		return nil, errors.WithDetails("hook event does not hold a list of matchers",
			"path", path, "event", event,
			"hint", "fix or remove the event by hand — refusing to overwrite it")
	}
	return matchers, nil
}

// hookCommandRegistered reports whether command already appears under any of an
// event's matchers. Entries of an unexpected shape are skipped rather than
// refused: this only asks a question, and a matcher it cannot read is one that
// cannot be the command being looked for.
func hookCommandRegistered(matchers []any, command string) bool {
	for _, m := range matchers {
		matcherObj, ok := m.(map[string]any)
		if !ok {
			continue
		}
		hookList, _ := matcherObj["hooks"].([]any)
		for _, h := range hookList {
			hookDef, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hookDef["command"].(string); cmd == command {
				return true
			}
		}
	}
	return false
}

// Save writes the document back. An unchanged document is not written at all,
// so a settings file nobody edited keeps its own formatting.
func (s *Settings) Save() error {
	if !s.changed {
		return nil
	}
	out, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling settings.json", "path", s.path)
	}
	out = append(out, '\n')
	if err := s.fw.WriteFile(s.path, out, 0o644); err != nil {
		return errors.WrapWithDetails(err, "writing settings.json", "path", s.path)
	}
	return nil
}
