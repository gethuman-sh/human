package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/botidentity"
)

// gitIdentityEnvKeys are the git env vars Claude Code injects into every
// Bash-tool invocation so the agent's git commits attribute to the bot. They
// mirror botidentity.Identity.GitEnv (SC-371) — the same mechanism the
// containerised exec and deployer rebase already use — applied here on the
// host developer-machine seam that those two paths do not cover (SC-2854).
var gitIdentityEnvKeys = []string{
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
}

// InstallGitIdentity resolves the bot identity from the project .humanconfig
// (cwd) and writes it into the project .claude/settings.json env block. Keying
// off Claude Code's per-session env — not a repo git config — means an agent's
// git commits attribute to the bot while a developer's own terminal commits in
// the same repo keep their identity. A .humanconfig read failure falls back to
// the default identity and never fails the install, mirroring the container
// seam at internal/agent/manager.go:227.
func InstallGitIdentity(w io.Writer, fw FileWriter) error {
	id, loadErr := botidentity.Load(".")
	if loadErr != nil {
		id = botidentity.Identity{Name: botidentity.DefaultName, Email: botidentity.DefaultEmail}
	}
	// Always the project file: the identity comes from the per-project bot
	// section, so a global env block would wrongly attribute the developer's
	// Claude-assisted commits in unrelated projects to the bot.
	path := filepath.Join(".claude", "settings.json")
	return mergeGitIdentityIntoSettings(w, fw, path, id)
}

func mergeGitIdentityIntoSettings(w io.Writer, fw FileWriter, path string, id botidentity.Identity) error {
	settings := make(map[string]any)

	data, err := fw.ReadFile(path)
	if err == nil {
		if jsonErr := json.Unmarshal(data, &settings); jsonErr != nil {
			return errors.WrapWithDetails(jsonErr, "parsing settings.json", "path", path)
		}
	} else if !os.IsNotExist(err) {
		return errors.WrapWithDetails(err, "reading settings.json", "path", path)
	}

	envMap, _ := settings["env"].(map[string]any)
	if envMap == nil {
		envMap = make(map[string]any)
	}

	desired := map[string]string{
		"GIT_AUTHOR_NAME":     id.Name,
		"GIT_AUTHOR_EMAIL":    id.Email,
		"GIT_COMMITTER_NAME":  id.Name,
		"GIT_COMMITTER_EMAIL": id.Email,
	}

	changed := false
	for _, k := range gitIdentityEnvKeys {
		if cur, _ := envMap[k].(string); cur != desired[k] {
			envMap[k] = desired[k]
			changed = true
		}
	}

	if !changed {
		_, _ = fmt.Fprintf(w, "  unchanged %s (bot git identity already set)\n", path)
		return nil
	}

	settings["env"] = envMap

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling settings.json")
	}
	out = append(out, '\n')

	if err := fw.WriteFile(path, out, 0o644); err != nil {
		return errors.WrapWithDetails(err, "writing settings.json", "path", path)
	}

	_, _ = fmt.Fprintf(w, "  updated %s (bot git identity set)\n", path)
	return nil
}
