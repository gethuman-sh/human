package jira

import (
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Config holds the configuration for a single Jira instance.
type Config struct {
	Name        string   `mapstructure:"name"`
	URL         string   `mapstructure:"url"`
	User        string   `mapstructure:"user"`
	Key         string   `mapstructure:"key"`
	Description string   `mapstructure:"description"`
	Role        string   `mapstructure:"role"`
	Safe        bool     `mapstructure:"safe"`
	Projects    []string `mapstructure:"projects"`
}

// LoadConfigs reads a .humanconfig YAML file from dir and returns the
// list of configured Jira instances. Returns nil and no error if the file
// does not exist.
func LoadConfigs(dir string) ([]Config, error) {
	var configs []Config
	if err := config.UnmarshalSection(dir, "jiras", &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// instanceSpec defines how Jira configs are loaded and built.
var instanceSpec = config.InstanceSpec[Config, tracker.Instance]{
	Section:   "jiras",
	EnvPrefix: "JIRA_",
	EnvFields: []config.EnvField[Config]{
		{Suffix: "URL", Set: func(c *Config, v string) { c.URL = v }, Get: func(c Config) string { return c.URL }},
		{Suffix: "USER", Set: func(c *Config, v string) { c.User = v }, Get: func(c Config) string { return c.User }},
		{Suffix: "KEY", Set: func(c *Config, v string) { c.Key = v }, Get: func(c Config) string { return c.Key }},
	},
	GetName: func(c Config) string { return c.Name },
	SetURL:  func(c *Config, v string) { c.URL = v },
	GetURL:  func(c Config) string { return c.URL },
	Build: func(cfg Config) (tracker.Instance, bool) {
		if cfg.URL == "" || cfg.User == "" || cfg.Key == "" {
			return tracker.Instance{}, false
		}
		return tracker.Instance{
			Name:        cfg.Name,
			Kind:        "jira",
			URL:         cfg.URL,
			User:        cfg.User,
			Description: cfg.Description,
			Role:        cfg.Role,
			Safe:        cfg.Safe,
			Projects:    cfg.Projects,
			Provider:    New(cfg.URL, cfg.User, cfg.Key),
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
