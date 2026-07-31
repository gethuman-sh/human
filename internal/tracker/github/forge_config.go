package github

import (
	"github.com/gethuman-sh/human/internal/config"
	forgegithub "github.com/gethuman-sh/human/internal/forge/github"
	"github.com/gethuman-sh/human/internal/tracker"
)

// ForgeConfig holds one forges: entry — a code-host backend used only to open
// pull requests. It deliberately carries no projects: a forge entry contributes
// no tracker instance, so it is invisible to tracker resolution, kind counting
// and issue listing while still serving `human pr create` ([SC-1671]).
//
// GitHub is currently the only backend that is a forge, so a forges: entry
// always resolves to a GitHub forge client; the Kind field is accepted (and
// defaulted) so the section can grow to other forges without a config break.
type ForgeConfig struct {
	Name  string `mapstructure:"name"`
	Kind  string `mapstructure:"kind"`
	URL   string `mapstructure:"url"`
	Token string `mapstructure:"token"`
}

// forgeInstanceSpec loads the forges: section into forge-only tracker.Instances.
var forgeInstanceSpec = config.InstanceSpec[ForgeConfig, tracker.Instance]{
	Section:    "forges",
	EnvPrefix:  "FORGE_",
	DefaultURL: "https://api.github.com",
	EnvFields: []config.EnvField[ForgeConfig]{
		{Suffix: "URL", Set: func(c *ForgeConfig, v string) { c.URL = v }, Get: func(c ForgeConfig) string { return c.URL }},
		{Suffix: "TOKEN", Set: func(c *ForgeConfig, v string) { c.Token = v }, Get: func(c ForgeConfig) string { return c.Token }},
	},
	GetName: func(c ForgeConfig) string { return c.Name },
	SetURL:  func(c *ForgeConfig, v string) { c.URL = v },
	GetURL:  func(c ForgeConfig) string { return c.URL },
	Build: func(cfg ForgeConfig) (tracker.Instance, bool) {
		if cfg.Token == "" {
			return tracker.Instance{}, false
		}
		return tracker.Instance{
			Name: cfg.Name,
			Kind: "github",
			URL:  cfg.URL,
			// A forge entry has no tracker Provider on purpose — the nil Provider
			// is what keeps it out of every tracker-facing code path.
			Role:  tracker.RoleForge,
			Forge: forgegithub.New(cfg.URL, cfg.Token),
		}, true
	},
}

// LoadForgeInstances reads the forges: section and builds forge-only instances.
func LoadForgeInstances(dir string) ([]tracker.Instance, error) {
	return config.LoadInstances(dir, forgeInstanceSpec)
}

// LoadForgeInstancesWithResolver is LoadForgeInstances with a per-project env
// lookup and vault secret resolver, matching the tracker loaders' signature so
// it can join the shared loader list.
func LoadForgeInstancesWithResolver(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]tracker.Instance, error) {
	spec := forgeInstanceSpec
	spec.Lookup = lookup
	spec.SecretResolver = resolver
	return config.LoadInstances(dir, spec)
}
