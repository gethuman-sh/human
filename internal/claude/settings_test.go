package claude

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingsFS is a FileWriter over an in-memory file set, with an optional read
// error for the "the file exists but cannot be read right now" case.
type settingsFS struct {
	files     map[string][]byte
	readErr   error
	writeErr  error
	writeSeen int
}

func newSettingsFS(files map[string][]byte) *settingsFS {
	if files == nil {
		files = map[string][]byte{}
	}
	return &settingsFS{files: files}
}

func (f *settingsFS) MkdirAll(string, os.FileMode) error { return nil }

func (f *settingsFS) ReadFile(name string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	data, ok := f.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return data, nil
}

func (f *settingsFS) WriteFile(name string, data []byte, _ os.FileMode) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writeSeen++
	f.files[name] = data
	return nil
}

const settingsPath = "/home/dev/.claude/settings.json"

// A settings key holding something other than the expected shape is the user's
// data. Reading it with a type assertion whose failure is silent, building a
// fresh map and writing it back is how `human install` deleted an env block it
// could not parse — with nothing said, on a file `human` edits but does not own.
func TestSettings_RefusesToOverwriteAKeyOfTheWrongShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		edit func(*Settings) error
	}{
		{
			name: "env is not an object",
			file: `{"env": ["GIT_AUTHOR_NAME=someone"]}`,
			edit: func(s *Settings) error { return s.SetEnv("GIT_AUTHOR_NAME", "humanbot") },
		},
		{
			name: "hooks is not an object",
			file: `{"hooks": "see the other file"}`,
			edit: func(s *Settings) error { return s.AddHook("Stop", "human hook", true, "") },
		},
		{
			name: "a hook event is not a list of matchers",
			file: `{"hooks": {"Stop": {"command": "something-else"}}}`,
			edit: func(s *Settings) error { return s.AddHook("Stop", "human hook", true, "") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newSettingsFS(map[string][]byte{settingsPath: []byte(tc.file)})
			settings, err := LoadSettings(fs, settingsPath)
			require.NoError(t, err)

			require.Error(t, tc.edit(settings), "a key of the wrong shape must be reported, not replaced")
			require.NoError(t, settings.Save())
			assert.Zero(t, fs.writeSeen, "nothing may be written after a refusal")
			assert.Equal(t, tc.file, string(fs.files[settingsPath]), "the user's file is untouched")
		})
	}
}

// A read that fails for any reason other than "not found" must not become an
// empty document — a settings file behind a permission error or a stalled
// mount would be replaced by the write that follows.
func TestSettings_AnUnreadableFileIsNotAnEmptyOne(t *testing.T) {
	fs := newSettingsFS(nil)
	fs.readErr = errors.New("permission denied")

	_, err := LoadSettings(fs, settingsPath)
	require.Error(t, err)
}

// A missing file IS an empty document: the first install writes one.
func TestSettings_AMissingFileStartsEmpty(t *testing.T) {
	fs := newSettingsFS(nil)
	settings, err := LoadSettings(fs, settingsPath)
	require.NoError(t, err)

	require.NoError(t, settings.SetEnv("ENABLE_LSP_TOOL", "1"))
	require.NoError(t, settings.Save())

	value, ok := settings.EnvValue("ENABLE_LSP_TOOL")
	assert.True(t, ok)
	assert.Equal(t, "1", value)
	assert.Equal(t, 1, fs.writeSeen)
}

// Everything `human` does not know about round-trips untouched. The file is the
// user's, and an editor that drops the parts it has no opinion on is worse than
// one that refuses to run.
func TestSettings_KeysHumanDoesNotKnowAboutSurvive(t *testing.T) {
	original := `{"model":"opus","permissions":{"allow":["Bash(ls:*)"]},"env":{"EDITOR":"vim"}}`
	fs := newSettingsFS(map[string][]byte{settingsPath: []byte(original)})

	settings, err := LoadSettings(fs, settingsPath)
	require.NoError(t, err)
	require.NoError(t, settings.SetEnv("ENABLE_LSP_TOOL", "1"))
	require.NoError(t, settings.Save())

	var written map[string]any
	require.NoError(t, json.Unmarshal(fs.files[settingsPath], &written))
	assert.Equal(t, "opus", written["model"])
	assert.NotNil(t, written["permissions"])
	assert.Equal(t, map[string]any{"EDITOR": "vim", "ENABLE_LSP_TOOL": "1"}, written["env"],
		"a new variable joins the env block rather than replacing it")
}

// A document nothing changed is not written at all, so running `human install`
// twice leaves the second run's file byte-for-byte as the user last had it.
func TestSettings_AnUnchangedDocumentIsNotRewritten(t *testing.T) {
	fs := newSettingsFS(map[string][]byte{settingsPath: []byte(`{"env":{"ENABLE_LSP_TOOL":"1"}}`)})

	settings, err := LoadSettings(fs, settingsPath)
	require.NoError(t, err)
	require.NoError(t, settings.SetEnv("ENABLE_LSP_TOOL", "1"))

	assert.False(t, settings.Changed(), "setting a value to what it already is changes nothing")
	require.NoError(t, settings.Save())
	assert.Zero(t, fs.writeSeen)
}

// Installing the same hook twice adds one registration, and a second command on
// the same event joins it rather than replacing it.
func TestSettings_AddHookIsIdempotentAndAdditive(t *testing.T) {
	fs := newSettingsFS(nil)
	settings, err := LoadSettings(fs, settingsPath)
	require.NoError(t, err)

	require.NoError(t, settings.AddHook("SessionStart", "human hook", true, ""))
	require.NoError(t, settings.AddHook("SessionStart", "human hook", true, ""))
	require.NoError(t, settings.AddHook("SessionStart", "human agent-context --hook", false, ""))
	require.NoError(t, settings.Save())

	var written struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(fs.files[settingsPath], &written))
	require.Len(t, written.Hooks["SessionStart"], 2, "the same command must not register twice")
	assert.Equal(t, "human hook", written.Hooks["SessionStart"][0].Hooks[0].Command)
	assert.True(t, written.Hooks["SessionStart"][0].Hooks[0].Async)
	assert.Equal(t, "human agent-context --hook", written.Hooks["SessionStart"][1].Hooks[0].Command)
	assert.False(t, written.Hooks["SessionStart"][1].Hooks[0].Async,
		"the context hook is blocking — Claude Code reads its output as context")
}
