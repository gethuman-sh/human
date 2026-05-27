package proxy

import (
	"github.com/gethuman-sh/human/internal/config"
)

// Config holds the proxy section of .humanconfig.yaml.
type Config struct {
	Mode      Mode     `mapstructure:"mode"`
	Domains   []string `mapstructure:"domains"`
	Intercept []string `mapstructure:"intercept"` // domains to MITM for traffic logging
}

// LoadConfig reads the proxy configuration from .humanconfig.yaml in dir.
// Returns (nil, nil) when the proxy section is absent.
func LoadConfig(dir string) (*Config, error) {
	var cfg Config
	if err := config.UnmarshalSection(dir, "proxy", &cfg); err != nil {
		return nil, err
	}

	if cfg.Mode == "" && len(cfg.Domains) == 0 {
		return nil, nil
	}

	return &cfg, nil
}
