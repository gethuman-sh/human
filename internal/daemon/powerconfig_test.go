package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPowerConfig_Missing(t *testing.T) {
	cfg, err := LoadPowerConfig(t.TempDir())
	require.NoError(t, err)
	assert.False(t, cfg.InhibitSleep, "a missing config must default to off")
}

func TestLoadPowerConfig_True(t *testing.T) {
	dir := t.TempDir()
	yaml := "power:\n  inhibit_sleep: true\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	cfg, err := LoadPowerConfig(dir)
	require.NoError(t, err)
	assert.True(t, cfg.InhibitSleep)
}

func TestLoadPowerConfig_False(t *testing.T) {
	dir := t.TempDir()
	yaml := "power:\n  inhibit_sleep: false\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o644))

	cfg, err := LoadPowerConfig(dir)
	require.NoError(t, err)
	assert.False(t, cfg.InhibitSleep)
}
