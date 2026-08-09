package github

import (
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Config holds the configuration for a single GitHub instance.
type Config struct {
	Name        string   `mapstructure:"name"`
	URL         string   `mapstructure:"url"`
	Token       string   `mapstructure:"token"`
	Description string   `mapstructure:"description"`
	Role        string   `mapstructure:"role"`
	Safe        bool     `mapstructure:"safe"`
	Projects    []string `mapstructure:"projects"`
	CreateIn    string   `mapstructure:"create_in"`
}

// LoadConfigs reads a .humanconfig YAML file from dir and returns the
// list of configured GitHub instances. Returns nil and no error if the file
// does not exist.
func LoadConfigs(dir string) ([]Config, error) {
	var configs []Config
	if err := config.UnmarshalSection(dir, "githubs", &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// instanceSpec defines how GitHub configs are loaded and built.
var instanceSpec = config.InstanceSpec[Config, tracker.Instance]{
	Section:    "githubs",
	Kind:       "github",
	EnvPrefix:  "GITHUB_",
	DefaultURL: "https://api.github.com",
	EnvFields: []config.EnvField[Config]{
		{Suffix: "URL", Set: func(c *Config, v string) { c.URL = v }, Get: func(c Config) string { return c.URL }},
		{Suffix: "TOKEN", Set: func(c *Config, v string) { c.Token = v }, Get: func(c Config) string { return c.Token }},
	},
	GetName: func(c Config) string { return c.Name },
	SetURL:  func(c *Config, v string) { c.URL = v },
	GetURL:  func(c Config) string { return c.URL },
	Build:   buildInstance,
}

// buildInstance turns one githubs: entry into a tracker instance.
//
// A githubs: entry is an issue tracker, and only that. GitHub's pull-request
// capability is configured as a forge, in the forges: section, loaded by
// internal/forge/github into a type of its own — so this function has no role to
// branch on and nothing to decide ([SC-3876]). The dual identity it used to
// build is what made a GitHub entry two things at once, and every caller then
// had to ask which.
func buildInstance(cfg Config) (tracker.Instance, bool) {
	if cfg.Token == "" {
		return tracker.Instance{}, false
	}
	return tracker.Instance{
		Name:        cfg.Name,
		Kind:        "github",
		URL:         cfg.URL,
		Description: cfg.Description,
		Role:        cfg.Role,
		Safe:        cfg.Safe,
		Projects:    cfg.Projects,
		CreateIn:    cfg.CreateIn,
		Provider:    New(cfg.URL, cfg.Token),
	}, true
}

// rejectForgeRole fails a load whose githubs: entry still declares role: forge.
//
// That role meant "this entry is a code host, not a tracker", and the section no
// longer has a way to say it — a forge is configured under forges:. Left to
// build, the entry would become a tracker carrying an unknown role, which is the
// worst of the three outcomes: it would join PM resolution and could quietly
// become the board's tracker. Failing the load puts the message where the user
// is already looking, since a load failure reaches the board's error banner
// ([SC-3876]).
//
// The condition and its wording come from config, which is also where the whole
// document is checked ([SC-3889]). They were separate strings, and a rule whose
// enforcement carries its own copy of itself is a rule with two versions waiting
// to disagree — the settings screen offered this very value while this loader
// rejected it.
func rejectForgeRole(instances []tracker.Instance, err error) ([]tracker.Instance, error) {
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.Role != config.RetiredForgeRole {
			continue
		}
		return nil, errors.WithDetails(config.RetiredForgeRoleMessage(inst.Name),
			"instance", inst.Name, "section", "githubs")
	}
	return instances, nil
}

// LoadInstances reads config, applies env overrides, creates clients,
// and returns ready-to-use tracker instances.
func LoadInstances(dir string) ([]tracker.Instance, error) {
	return rejectForgeRole(config.LoadInstances(dir, instanceSpec))
}

// LoadInstancesWithLookup is like LoadInstances but uses a custom env lookup
// function for per-project token scoping.
func LoadInstancesWithLookup(dir string, lookup config.EnvLookup) ([]tracker.Instance, error) {
	spec := instanceSpec
	spec.Lookup = lookup
	return rejectForgeRole(config.LoadInstances(dir, spec))
}

// LoadInstancesWithResolver is like LoadInstances but uses a custom env lookup
// and a vault secret resolver for 1pw:// references.
func LoadInstancesWithResolver(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]tracker.Instance, error) {
	spec := instanceSpec
	spec.Lookup = lookup
	spec.SecretResolver = resolver
	return rejectForgeRole(config.LoadInstances(dir, spec))
}
