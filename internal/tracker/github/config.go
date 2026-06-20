package github

import (
	"github.com/gethuman-sh/human/internal/config"
	forgegithub "github.com/gethuman-sh/human/internal/forge/github"
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
	EnvPrefix:  "GITHUB_",
	DefaultURL: "https://api.github.com",
	EnvFields: []config.EnvField[Config]{
		{Suffix: "URL", Set: func(c *Config, v string) { c.URL = v }, Get: func(c Config) string { return c.URL }},
		{Suffix: "TOKEN", Set: func(c *Config, v string) { c.Token = v }, Get: func(c Config) string { return c.Token }},
	},
	GetName: func(c Config) string { return c.Name },
	SetURL:  func(c *Config, v string) { c.URL = v },
	GetURL:  func(c Config) string { return c.URL },
	Build: func(cfg Config) (tracker.Instance, bool) {
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
			Provider:    New(cfg.URL, cfg.Token),
			// GitHub is also a code forge — expose PR creation alongside the
			// issue-tracker provider via a separate forge client.
			Forge: forgegithub.New(cfg.URL, cfg.Token),
		}, true
	},
}

// LoadInstances reads config, applies env overrides, creates clients,
// and returns ready-to-use tracker instances.
func LoadInstances(dir string) ([]tracker.Instance, error) {
	return config.LoadInstances(dir, instanceSpec)
}

// LoadInstancesWithLookup is like LoadInstances but uses a custom env lookup
// function for per-project token scoping.
func LoadInstancesWithLookup(dir string, lookup config.EnvLookup) ([]tracker.Instance, error) {
	spec := instanceSpec
	spec.Lookup = lookup
	return config.LoadInstances(dir, spec)
}

// LoadInstancesWithResolver is like LoadInstances but uses a custom env lookup
// and a vault secret resolver for 1pw:// references.
func LoadInstancesWithResolver(dir string, lookup config.EnvLookup, resolver config.SecretResolveFunc) ([]tracker.Instance, error) {
	spec := instanceSpec
	spec.Lookup = lookup
	spec.SecretResolver = resolver
	return config.LoadInstances(dir, spec)
}
