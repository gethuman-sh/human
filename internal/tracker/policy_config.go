package tracker

import (
	"github.com/gethuman-sh/human/internal/config"
)

// LoadPolicyConfig reads the policies section from .humanconfig.yaml in dir.
// Returns (nil, nil) when the policies section is absent or empty.
func LoadPolicyConfig(dir string) (*PolicyConfig, error) {
	var cfg PolicyConfig
	if err := config.UnmarshalSection(dir, "policies", &cfg); err != nil {
		return nil, err
	}

	if len(cfg.Block) == 0 && len(cfg.Confirm) == 0 {
		return nil, nil
	}

	return &cfg, nil
}
