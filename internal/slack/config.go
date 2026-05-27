package slack

import (
	"github.com/gethuman-sh/human/internal/config"
)

// Config holds the configuration for a single Slack instance.
type Config struct {
	Name        string `mapstructure:"name"`
	Token       string `mapstructure:"token"`
	Channel     string `mapstructure:"channel"`
	Description string `mapstructure:"description"`
}

// Instance represents a configured Slack bot ready for use.
type Instance struct {
	Name        string
	Description string
	Channel     string
	Client      *Client
}

// LoadConfigs reads a .humanconfig YAML file from dir and returns the
// list of configured Slack instances. Returns nil and no error if the file
// does not exist.
func LoadConfigs(dir string) ([]Config, error) {
	var configs []Config
	if err := config.UnmarshalSection(dir, "slacks", &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// instanceSpec defines how Slack configs are loaded and built.
var instanceSpec = config.InstanceSpec[Config, Instance]{
	Section:   "slacks",
	EnvPrefix: "SLACK_",
	EnvFields: []config.EnvField[Config]{
		{Suffix: "TOKEN", Set: func(c *Config, v string) { c.Token = v }, Get: func(c Config) string { return c.Token }},
	},
	GetName: func(c Config) string { return c.Name },
	Build: func(cfg Config) (Instance, bool) {
		if cfg.Token == "" || cfg.Channel == "" {
			return Instance{}, false
		}
		return Instance{
			Name:        cfg.Name,
			Description: cfg.Description,
			Channel:     cfg.Channel,
			Client:      New(cfg.Token, cfg.Channel),
		}, true
	},
}

// LoadInstances reads config, applies env overrides, creates clients,
// and returns ready-to-use Slack instances.
func LoadInstances(dir string) ([]Instance, error) {
	return config.LoadInstances(dir, instanceSpec)
}

// LoadInstancesWithResolver is like LoadInstances but uses a vault secret
// resolver for 1pw:// references.
func LoadInstancesWithResolver(dir string, resolver config.SecretResolveFunc) ([]Instance, error) {
	spec := instanceSpec
	spec.SecretResolver = resolver
	return config.LoadInstances(dir, spec)
}
