package vault

import (
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/platform"
)

// Config holds the vault configuration from .humanconfig.
type Config struct {
	Provider string `mapstructure:"provider"`
	Account  string `mapstructure:"account"`
}

// ReadConfig reads the vault section from .humanconfig in dir.
// Returns (nil, nil) when the config file is absent or when the file
// is present but has no vault section. Returns a non-nil error when the
// config file itself fails to parse — the caller must decide whether to
// fail or continue without vault resolution.
func ReadConfig(dir string) (*Config, error) {
	var cfg Config
	if err := config.UnmarshalSection(dir, "vault", &cfg); err != nil {
		return nil, errors.WrapWithDetails(err, "reading vault section", "dir", dir)
	}
	if cfg.Provider == "" {
		return nil, nil
	}
	return &cfg, nil
}

// NewResolverFromConfig creates a Resolver based on the vault configuration.
// Returns nil if cfg is nil or the provider is unrecognized (graceful no-op).
// The GitHub CLI provider needs no account or app integration, so gh://
// references resolve under every configured provider — a 1Password setup can
// mix 1pw:// and gh:// references freely.
func NewResolverFromConfig(cfg *Config) *Resolver {
	if cfg == nil {
		return nil
	}

	switch cfg.Provider {
	case "1password", "1pw":
		if platform.IsWSL() {
			return NewResolver(NewOpCLI(), NewGhCLI())
		}
		return NewResolver(NewOnePassword(cfg.Account), NewGhCLI())
	case "github", "gh":
		return NewResolver(NewGhCLI())
	default:
		return nil
	}
}
