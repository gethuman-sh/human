// Package botidentity resolves the bot identity that attributes pipeline agent
// commits, so authorship alone separates agent work from developer work. The
// identity is read from the .humanconfig "bot" section and falls back to a
// sensible default so attribution works before anyone configures anything.
package botidentity

import "github.com/gethuman-sh/human/internal/config"

// Default identity applied when .humanconfig has no bot section (or an empty
// field). The noreply domain matches the existing pipeline bot convention.
const (
	DefaultName  = "humanbot"
	DefaultEmail = "humanbot@users.noreply.gethuman.sh"
)

// Identity is the git author/committer attributed to agent commits.
type Identity struct {
	Name  string `mapstructure:"name"`
	Email string `mapstructure:"email"`
}

// Load reads the "bot" section from .humanconfig in dir, filling any empty
// field with its default. A missing config file yields the full default
// identity. Package var so callers can stub in tests.
var Load = func(dir string) (Identity, error) {
	var id Identity
	if err := config.UnmarshalSection(dir, "bot", &id); err != nil {
		return Identity{}, err
	}
	return id.withDefaults(), nil
}

func (i Identity) withDefaults() Identity {
	if i.Name == "" {
		i.Name = DefaultName
	}
	if i.Email == "" {
		i.Email = DefaultEmail
	}
	return i
}

// GitEnv returns the KEY=VALUE git identity env pairs that make git attribute
// both author and committer to the bot for any git invocation in the process.
func (i Identity) GitEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=" + i.Name,
		"GIT_AUTHOR_EMAIL=" + i.Email,
		"GIT_COMMITTER_NAME=" + i.Name,
		"GIT_COMMITTER_EMAIL=" + i.Email,
	}
}
