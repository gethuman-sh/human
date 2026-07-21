package daemon

import "github.com/gethuman-sh/human/internal/config"

// PowerConfig is the `power:` section of .humanconfig — daemon power-management
// toggles. It is read per-tick (not at startup) so a settings-UI change applies
// without a daemon restart.
type PowerConfig struct {
	// InhibitSleep, off by default, makes the daemon hold a systemd suspend
	// block while any agent runs so an auto-suspending desktop cannot freeze
	// the factory mid-pipeline.
	InhibitSleep bool `mapstructure:"inhibit_sleep"`
}

// LoadPowerConfig reads the power section from .humanconfig in dir. A missing
// file or section yields the zero value (InhibitSleep=false), matching the
// off-by-default contract.
func LoadPowerConfig(dir string) (PowerConfig, error) {
	var cfg PowerConfig
	if err := config.UnmarshalSection(dir, "power", &cfg); err != nil {
		return PowerConfig{}, err
	}
	return cfg, nil
}
