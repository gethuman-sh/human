package telegram

import (
	"fmt"

	"github.com/gethuman-sh/human/internal/config"
)

// Config holds the configuration for a single Telegram bot instance.
type Config struct {
	Name         string  `mapstructure:"name"`
	Token        string  `mapstructure:"token"`
	Description  string  `mapstructure:"description"`
	AllowedUsers []int64 `mapstructure:"allowed_users"`
	// AllowedChats is the set of group/supergroup/channel chat IDs allowed
	// to dispatch messages. Private chats (1:1 between user and bot) do not
	// need an entry here — being in AllowedUsers is sufficient. For group
	// dispatch this must be set explicitly, per-chat, as an opt-in.
	AllowedChats []int64 `mapstructure:"allowed_chats"`
	NotifyChatID int64   `mapstructure:"notify_chat_id"` // Chat ID for proactive notifications (destructive ops, etc.)
}

// Instance represents a configured Telegram bot ready for use.
type Instance struct {
	Name         string
	Description  string
	Client       *Client
	AllowedUsers []int64
	AllowedChats []int64 // see Config.AllowedChats
	NotifyChatID int64   // Chat ID for proactive notifications
}

// ConfigWarnings returns human-readable warnings about this instance's
// configuration relative to the auth rule enforced by IsAllowed. An empty
// slice means the configuration is healthy. Warnings surface silent
// misconfigurations like "Telegram is enabled but the allowlist is empty,
// so every message will be rejected" — the runtime behavior is still safe
// (default-deny), but operators deserve to know.
func (i *Instance) ConfigWarnings() []string {
	var warnings []string
	if len(i.AllowedUsers) == 0 {
		warnings = append(warnings, fmt.Sprintf(
			"Telegram instance %q has empty allowed_users; all messages will be rejected (default-deny). Add user IDs to allowed_users in .humanconfig.",
			i.Name,
		))
	}
	return warnings
}

// LoadConfigs reads a .humanconfig YAML file from dir and returns the
// list of configured Telegram instances. Returns nil and no error if the file
// does not exist.
func LoadConfigs(dir string) ([]Config, error) {
	var configs []Config
	if err := config.UnmarshalSection(dir, "telegrams", &configs); err != nil {
		return nil, err
	}
	return configs, nil
}

// instanceSpec defines how Telegram configs are loaded and built.
var instanceSpec = config.InstanceSpec[Config, Instance]{
	Section:   "telegrams",
	EnvPrefix: "TELEGRAM_",
	EnvFields: []config.EnvField[Config]{
		{Suffix: "TOKEN", Set: func(c *Config, v string) { c.Token = v }, Get: func(c Config) string { return c.Token }},
	},
	GetName: func(c Config) string { return c.Name },
	Build: func(cfg Config) (Instance, bool) {
		if cfg.Token == "" {
			return Instance{}, false
		}
		return Instance{
			Name:         cfg.Name,
			Description:  cfg.Description,
			Client:       New(cfg.Token),
			AllowedUsers: cfg.AllowedUsers,
			AllowedChats: cfg.AllowedChats,
			NotifyChatID: cfg.NotifyChatID,
		}, true
	},
}

// LoadInstances reads config, applies env overrides, creates clients,
// and returns ready-to-use Telegram instances.
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
