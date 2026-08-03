package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/botidentity"
)

// stubBotIdentity replaces the botidentity.Load package var for a single test
// and restores it afterwards, so tests never depend on the ambient .humanconfig.
func stubBotIdentity(t *testing.T, id botidentity.Identity, err error) {
	t.Helper()
	orig := botidentity.Load
	botidentity.Load = func(string) (botidentity.Identity, error) { return id, err }
	t.Cleanup(func() { botidentity.Load = orig })
}

// settingsEnv reads back the env block the code wrote to the project settings.
func settingsEnv(t *testing.T, fw *mockFileWriter) map[string]any {
	t.Helper()
	data, ok := fw.files[filepath.Join(".claude", "settings.json")]
	require.True(t, ok, "project .claude/settings.json should have been written")
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	env, ok := settings["env"].(map[string]any)
	require.True(t, ok, "settings should carry an env map")
	return env
}

func TestInstallGitIdentity_NewSettings(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	require.NoError(t, InstallGitIdentity(&buf, fw))

	env := settingsEnv(t, fw)
	assert.Equal(t, "botx", env["GIT_AUTHOR_NAME"])
	assert.Equal(t, "botx@e", env["GIT_AUTHOR_EMAIL"])
	assert.Equal(t, "botx", env["GIT_COMMITTER_NAME"])
	assert.Equal(t, "botx@e", env["GIT_COMMITTER_EMAIL"])
	assert.Contains(t, buf.String(), "bot git identity set")
}

func TestInstallGitIdentity_PreservesUnrelatedEnv(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()
	fw.files[filepath.Join(".claude", "settings.json")] = []byte(`{"env":{"FOO":"bar"}}`)
	var buf bytes.Buffer

	require.NoError(t, InstallGitIdentity(&buf, fw))

	env := settingsEnv(t, fw)
	assert.Equal(t, "bar", env["FOO"], "unrelated env keys must survive")
	assert.Equal(t, "botx", env["GIT_AUTHOR_NAME"])
	assert.Equal(t, "botx@e", env["GIT_COMMITTER_EMAIL"])
}

func TestInstallGitIdentity_PreservesOtherTopLevelKeys(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()
	fw.files[filepath.Join(".claude", "settings.json")] = []byte(`{"hooks":{"SessionStart":[]},"env":{}}`)
	var buf bytes.Buffer

	require.NoError(t, InstallGitIdentity(&buf, fw))

	data := fw.files[filepath.Join(".claude", "settings.json")]
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	_, hasHooks := settings["hooks"]
	assert.True(t, hasHooks, "unrelated top-level keys (hooks) must survive")

	env := settingsEnv(t, fw)
	assert.Equal(t, "botx", env["GIT_AUTHOR_NAME"])
}

func TestInstallGitIdentity_Idempotent(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()

	var first bytes.Buffer
	require.NoError(t, InstallGitIdentity(&first, fw))
	firstBytes := append([]byte(nil), fw.files[filepath.Join(".claude", "settings.json")]...)

	var second bytes.Buffer
	require.NoError(t, InstallGitIdentity(&second, fw))

	assert.Contains(t, second.String(), "unchanged")
	assert.Equal(t, firstBytes, fw.files[filepath.Join(".claude", "settings.json")],
		"a second run must not rewrite the file")
}

func TestInstallGitIdentity_LoadErrorFallsBackToDefault(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{}, fmt.Errorf("no config"))
	fw := newMockFileWriter()
	var buf bytes.Buffer

	require.NoError(t, InstallGitIdentity(&buf, fw), "a config read failure must never fail install")

	env := settingsEnv(t, fw)
	assert.Equal(t, botidentity.DefaultName, env["GIT_AUTHOR_NAME"])
	assert.Equal(t, botidentity.DefaultEmail, env["GIT_AUTHOR_EMAIL"])
	assert.Equal(t, botidentity.DefaultName, env["GIT_COMMITTER_NAME"])
	assert.Equal(t, botidentity.DefaultEmail, env["GIT_COMMITTER_EMAIL"])
}

func TestInstallGitIdentity_PropagatesNonNotExistReadError(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()
	fw.readFn = func(string) ([]byte, error) { return nil, os.ErrPermission }
	var buf bytes.Buffer

	err := InstallGitIdentity(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading settings.json")
	_, wrote := fw.files[filepath.Join(".claude", "settings.json")]
	assert.False(t, wrote, "a readable-but-broken file must not be clobbered")
}

func TestInstallGitIdentity_MalformedSettingsError(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()
	fw.files[filepath.Join(".claude", "settings.json")] = []byte(`{bad`)
	var buf bytes.Buffer

	err := InstallGitIdentity(&buf, fw)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing settings.json")
}

func TestInstall_WritesProjectGitIdentityEvenWhenPersonal(t *testing.T) {
	stubBotIdentity(t, botidentity.Identity{Name: "botx", Email: "botx@e"}, nil)
	fw := newMockFileWriter()
	var buf bytes.Buffer

	require.NoError(t, Install(&buf, fw, true))

	// The git-identity settings file is the project one (relative), never home.
	projectPath := filepath.Join(".claude", "settings.json")
	data, ok := fw.files[projectPath]
	require.True(t, ok, "project git-identity settings.json must exist even under --personal")
	assert.False(t, filepath.IsAbs(projectPath))

	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	env, ok := settings["env"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "botx", env["GIT_AUTHOR_NAME"])
	assert.Equal(t, "botx@e", env["GIT_AUTHOR_EMAIL"])
	assert.Equal(t, "botx", env["GIT_COMMITTER_NAME"])
	assert.Equal(t, "botx@e", env["GIT_COMMITTER_EMAIL"])
}
