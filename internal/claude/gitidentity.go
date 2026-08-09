package claude

import (
	"fmt"
	"io"
	"path/filepath"

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
	settings, err := LoadSettings(fw, path)
	if err != nil {
		return err
	}

	desired := map[string]string{
		"GIT_AUTHOR_NAME":     id.Name,
		"GIT_AUTHOR_EMAIL":    id.Email,
		"GIT_COMMITTER_NAME":  id.Name,
		"GIT_COMMITTER_EMAIL": id.Email,
	}
	for _, k := range gitIdentityEnvKeys {
		if err := settings.SetEnv(k, desired[k]); err != nil {
			return err
		}
	}

	if !settings.Changed() {
		_, _ = fmt.Fprintf(w, "  unchanged %s (bot git identity already set)\n", path)
		return nil
	}
	if err := settings.Save(); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "  updated %s (bot git identity set)\n", path)
	return nil
}
