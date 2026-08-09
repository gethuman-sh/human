package github

import (
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/forge"
)

// Config holds one forges: entry — a code host used to open pull requests.
//
// It carries no projects, no role and no safe flag: those are issue-tracker
// concepts, and a forge entry that accepted them would be inviting the union
// this package exists without ([SC-3876]).
//
// GitHub is currently the only backend that is a forge, so a forges: entry
// always resolves to a GitHub client; Kind is accepted (and defaulted) so the
// section can grow to other forges without a config break.
type Config struct {
	Name  string `mapstructure:"name"`
	Kind  string `mapstructure:"kind"`
	URL   string `mapstructure:"url"`
	Token string `mapstructure:"token"`
}

// instanceSpec loads the forges: section into forge instances.
var instanceSpec = config.InstanceSpec[Config, forge.Instance]{
	Section:    "forges",
	EnvPrefix:  "FORGE_",
	DefaultURL: "https://api.github.com",
	EnvFields: []config.EnvField[Config]{
		{Suffix: "URL", Set: func(c *Config, v string) { c.URL = v }, Get: func(c Config) string { return c.URL }},
		{Suffix: "TOKEN", Set: func(c *Config, v string) { c.Token = v }, Get: func(c Config) string { return c.Token }},
	},
	GetName: func(c Config) string { return c.Name },
	SetURL:  func(c *Config, v string) { c.URL = v },
	GetURL:  func(c Config) string { return c.URL },
	Build: func(cfg Config) (forge.Instance, bool) {
		if cfg.Token == "" {
			return forge.Instance{}, false
		}
		kind := cfg.Kind
		if kind == "" {
			kind = "github"
		}
		return forge.Instance{
			Name:  cfg.Name,
			Kind:  kind,
			URL:   cfg.URL,
			Forge: New(cfg.URL, cfg.Token),
		}, true
	},
}

// LoadInstances reads the forges: section and builds forge instances.
func LoadInstances(dir string) ([]forge.Instance, error) {
	return config.LoadInstances(dir, instanceSpec)
}

// LoadInstancesWithResolver is LoadInstances with a per-project env lookup and a
// vault secret resolver, matching the tracker loaders' signature so it can join
// the shared loader list.
func LoadInstancesWithResolver(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]forge.Instance, error) {
	spec := instanceSpec
	spec.Lookup = lookup
	spec.SecretResolver = resolver
	return config.LoadInstances(dir, spec)
}
