package config

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"

	"github.com/gethuman-sh/human/errors"
)

// UnmarshalSection reads a .humanconfig YAML file from dir and unmarshals
// the given key into target. Returns nil when the config file is missing.
func UnmarshalSection(dir, key string, target any) error {
	v, err := readConfig(dir)
	if err != nil {
		return err
	}
	if v == nil {
		return nil
	}

	if err := v.UnmarshalKey(key, target); err != nil {
		return errors.WrapWithDetails(err, "parsing %s config", "section", key, "dir", dir)
	}

	return nil
}

// ReadProjectName reads the top-level "project" field from .humanconfig in dir.
// Returns "" if not set or config is missing.
func ReadProjectName(dir string) string {
	v, err := readConfig(dir)
	if err != nil || v == nil {
		return ""
	}
	return v.GetString("project")
}

// ConfigFileNames are the accepted project-config filenames, in the order
// viper's readConfig probes them via SetConfigName. Exported so callers that
// only need to check for a config file's presence (e.g. the desktop app's
// project picker) don't duplicate this list.
var ConfigFileNames = []string{".humanconfig.yaml", ".humanconfig.yml", ".humanconfig"}

// HasConfigFile reports whether dir directly contains one of the accepted
// .humanconfig filenames. Unlike readConfig it does not parse the file or
// search ancestor directories — it is a cheap existence check for callers
// (the desktop app's "open project" picker) that must validate a
// user-chosen directory before treating it as a project root.
func HasConfigFile(dir string) bool {
	for _, name := range ConfigFileNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// readConfig creates a viper instance and reads the .humanconfig file from
// dir (or its local/ subdirectory). Returns (nil, nil) when no config file exists.
func readConfig(dir string) (*viper.Viper, error) {
	v := viper.New()
	v.SetConfigName(".humanconfig")
	v.SetConfigType("yaml")
	v.AddConfigPath(dir)
	v.AddConfigPath(filepath.Join(dir, "local"))

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return nil, nil
		}
		return nil, errors.WrapWithDetails(err, "parsing config file", "dir", dir)
	}

	return v, nil
}
