package vault

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

// Config holds the vault configuration from .humanconfig.
type Config struct {
	Provider string `mapstructure:"provider"`
	Account  string `mapstructure:"account"`
	// CacheTTL bounds how long a resolved secret is kept in memory as a lapse
	// fallback, as a Go duration string (e.g. "15m", "0" to disable). Empty
	// means DefaultCacheTTL.
	CacheTTL string `mapstructure:"cache_ttl"`
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
	// Surface a malformed cache_ttl as a config error rather than silently
	// falling back, so the operator sees the mistake.
	if strings.TrimSpace(cfg.CacheTTL) != "" {
		if _, err := time.ParseDuration(cfg.CacheTTL); err != nil {
			return nil, errors.WrapWithDetails(err, "parsing vault cache_ttl", "value", cfg.CacheTTL)
		}
	}
	return &cfg, nil
}

// cacheTTL returns the configured cache TTL, or DefaultCacheTTL when unset.
// The value is validated at ReadConfig time, so a parse failure here falls
// back to the default rather than propagating.
func (c *Config) cacheTTL() time.Duration {
	if strings.TrimSpace(c.CacheTTL) == "" {
		return DefaultCacheTTL
	}
	d, err := time.ParseDuration(c.CacheTTL)
	if err != nil {
		return DefaultCacheTTL
	}
	return d
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

	var r *Resolver
	switch cfg.Provider {
	case "1password", "1pw":
		// SDK first (works in CGO dev builds), op CLI behind it as the
		// fallback that works in released CGO-disabled builds on every
		// platform, then gh:// for GitHub CLI references.
		r = NewResolver(NewOnePassword(cfg.Account), NewOpCLI(), NewGhCLI())
	case "github", "gh":
		r = NewResolver(NewGhCLI())
	default:
		return nil
	}
	r.ttl = cfg.cacheTTL()
	return r
}
