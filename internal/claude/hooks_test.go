package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallHooks_NewSettings(t *testing.T) {
	fw := newMockFileWriter()
	// ReadFile returns not-found for settings.json → treated as empty.
	fw.readFn = func(name string) ([]byte, error) {
		if filepath.Base(name) == "settings.json" {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrNotExist
	}

	var buf bytes.Buffer
	err := InstallHooks(&buf, fw)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "hooks registered")

	// Verify settings.json was written with hooks.
	var settingsPath string
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			settingsPath = path
			break
		}
	}
	require.NotEmpty(t, settingsPath, "settings.json should be written")

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(fw.files[settingsPath], &settings))

	hooks, ok := settings["hooks"].(map[string]interface{})
	require.True(t, ok, "hooks key should exist")

	// All 12 events registered. SessionStart carries two commands: the
	// monitoring `human hook` and the priming `human agent-context`.
	for _, evt := range []string{"UserPromptSubmit", "Stop", "SubagentStart", "SubagentStop",
		"PreToolUse", "PostToolUse", "PostToolUseFailure",
		"PermissionRequest", "Notification", "StopFailure", "SessionStart", "SessionEnd"} {
		matchers, ok := hooks[evt].([]interface{})
		require.True(t, ok, "event %s should have matchers", evt)
		want := 1
		if evt == "SessionStart" {
			want = 2
		}
		assert.Len(t, matchers, want, "event %s", evt)
	}

	// No hook script file should be written — hooks invoke `human hook` directly.
	for path := range fw.files {
		assert.NotEqual(t, "human-status-hook.sh", filepath.Base(path),
			"hook script should NOT be written")
	}
}

func TestInstallHooks_ExistingSettings(t *testing.T) {
	fw := newMockFileWriter()

	existingSettings := map[string]interface{}{
		"permissions": map[string]interface{}{
			"allow": []string{"WebSearch"},
		},
		"statusLine": map[string]interface{}{
			"type":    "command",
			"command": "bash ~/status.sh",
		},
	}
	existingJSON, _ := json.Marshal(existingSettings)

	fw.readFn = func(name string) ([]byte, error) {
		if filepath.Base(name) == "settings.json" {
			return existingJSON, nil
		}
		return nil, os.ErrNotExist
	}

	var buf bytes.Buffer
	err := InstallHooks(&buf, fw)

	require.NoError(t, err)

	// Find written settings.json.
	var settingsPath string
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			settingsPath = path
			break
		}
	}
	require.NotEmpty(t, settingsPath)

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(fw.files[settingsPath], &settings))

	// Existing fields preserved.
	perms, ok := settings["permissions"].(map[string]interface{})
	require.True(t, ok, "permissions should be preserved")
	assert.NotNil(t, perms["allow"])

	statusLine, ok := settings["statusLine"].(map[string]interface{})
	require.True(t, ok, "statusLine should be preserved")
	assert.Equal(t, "command", statusLine["type"])

	// Hooks added.
	_, ok = settings["hooks"].(map[string]interface{})
	assert.True(t, ok, "hooks should be added")
}

func TestInstallHooks_Idempotent(t *testing.T) {
	fw := newMockFileWriter()
	fw.readFn = func(name string) ([]byte, error) {
		if filepath.Base(name) == "settings.json" {
			return nil, os.ErrNotExist
		}
		return nil, os.ErrNotExist
	}

	// First install.
	var buf1 bytes.Buffer
	require.NoError(t, InstallHooks(&buf1, fw))

	// Save written settings for second call.
	var settingsPath string
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			settingsPath = path
			break
		}
	}
	firstSettings := fw.files[settingsPath]

	// Second install — reads back what was written.
	fw.readFn = func(name string) ([]byte, error) {
		if filepath.Base(name) == "settings.json" {
			return firstSettings, nil
		}
		if data, ok := fw.files[name]; ok {
			return data, nil
		}
		return nil, os.ErrNotExist
	}

	var buf2 bytes.Buffer
	require.NoError(t, InstallHooks(&buf2, fw))

	assert.Contains(t, buf2.String(), "hooks already registered")

	// Settings should not have duplicate matchers.
	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(fw.files[settingsPath], &settings))
	hooks := settings["hooks"].(map[string]interface{})
	for _, evt := range []string{"UserPromptSubmit", "Stop", "SubagentStart", "SubagentStop",
		"PreToolUse", "PostToolUse", "PostToolUseFailure",
		"PermissionRequest", "Notification", "StopFailure", "SessionStart", "SessionEnd"} {
		matchers := hooks[evt].([]interface{})
		want := 1
		if evt == "SessionStart" {
			want = 2 // human hook + human agent-context, still no duplicates
		}
		assert.Len(t, matchers, want, "event %s should not gain duplicate matchers", evt)
	}
}

func TestInstallHooks_MergesWithUserHooks(t *testing.T) {
	fw := newMockFileWriter()

	// User already has a Stop hook.
	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"Stop": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "echo user hook",
						},
					},
				},
			},
		},
	}
	existingJSON, _ := json.Marshal(existingSettings)

	fw.readFn = func(name string) ([]byte, error) {
		if filepath.Base(name) == "settings.json" {
			return existingJSON, nil
		}
		return nil, os.ErrNotExist
	}

	var buf bytes.Buffer
	require.NoError(t, InstallHooks(&buf, fw))

	var settingsPath string
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			settingsPath = path
			break
		}
	}

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(fw.files[settingsPath], &settings))
	hooks := settings["hooks"].(map[string]interface{})

	// Stop should have 2 matchers: user's + ours.
	stopMatchers := hooks["Stop"].([]interface{})
	assert.Len(t, stopMatchers, 2, "Stop should have user hook + our hook")

	// UserPromptSubmit should have 1 (only ours).
	promptMatchers := hooks["UserPromptSubmit"].([]interface{})
	assert.Len(t, promptMatchers, 1)
}

func TestInstallHooks_NoScriptFile(t *testing.T) {
	fw := newMockFileWriter()
	fw.readFn = func(_ string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	var buf bytes.Buffer
	require.NoError(t, InstallHooks(&buf, fw))

	// Only settings.json should be written — no script file.
	for path := range fw.files {
		assert.Equal(t, "settings.json", filepath.Base(path),
			"only settings.json should be written, got: %s", path)
	}
}

func TestInstallHooks_NotificationMatcher(t *testing.T) {
	fw := newMockFileWriter()
	fw.readFn = func(_ string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	var buf bytes.Buffer
	require.NoError(t, InstallHooks(&buf, fw))

	var settingsPath string
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			settingsPath = path
			break
		}
	}

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(fw.files[settingsPath], &settings))
	hooks := settings["hooks"].(map[string]interface{})

	// Notification should have matcher ".*" (not empty).
	matchers := hooks["Notification"].([]interface{})
	matcher := matchers[0].(map[string]interface{})
	assert.Equal(t, ".*", matcher["matcher"], "Notification hook should have .* matcher")

	// Other hooks should have empty matcher.
	stopMatchers := hooks["Stop"].([]interface{})
	stopMatcher := stopMatchers[0].(map[string]interface{})
	assert.Equal(t, "", stopMatcher["matcher"], "Stop hook should have empty matcher")
}

func TestInstallHooks_UserPromptSubmitNotAsync(t *testing.T) {
	fw := newMockFileWriter()
	fw.readFn = func(_ string) ([]byte, error) {
		return nil, os.ErrNotExist
	}

	var buf bytes.Buffer
	require.NoError(t, InstallHooks(&buf, fw))

	var settingsPath string
	for path := range fw.files {
		if filepath.Base(path) == "settings.json" {
			settingsPath = path
			break
		}
	}

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(fw.files[settingsPath], &settings))
	hooks := settings["hooks"].(map[string]interface{})

	// UserPromptSubmit should NOT have async.
	matchers := hooks["UserPromptSubmit"].([]interface{})
	matcher := matchers[0].(map[string]interface{})
	hookList := matcher["hooks"].([]interface{})
	hookDef := hookList[0].(map[string]interface{})
	_, hasAsync := hookDef["async"]
	assert.False(t, hasAsync, "UserPromptSubmit hook should not have async field")

	// Stop SHOULD have async: true.
	stopMatchers := hooks["Stop"].([]interface{})
	stopMatcher := stopMatchers[0].(map[string]interface{})
	stopHookList := stopMatcher["hooks"].([]interface{})
	stopHookDef := stopHookList[0].(map[string]interface{})
	assert.Equal(t, true, stopHookDef["async"], "Stop hook should have async: true")
}
