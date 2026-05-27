package tracker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func TestLoadPolicyConfig_HappyPath(t *testing.T) {
	dir := t.TempDir()
	content := `
policies:
  block:
    - delete
    - assign
  confirm:
    - transition:Done
    - create
`
	err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644)
	require.NoError(t, err)

	cfg, err := tracker.LoadPolicyConfig(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, []string{"delete", "assign"}, cfg.Block)
	assert.Equal(t, []string{"transition:Done", "create"}, cfg.Confirm)
}

func TestLoadPolicyConfig_MissingSection(t *testing.T) {
	dir := t.TempDir()
	content := `
jiras:
  - name: myorg
    url: https://example.com
`
	err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644)
	require.NoError(t, err)

	cfg, err := tracker.LoadPolicyConfig(dir)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestLoadPolicyConfig_EmptySection(t *testing.T) {
	dir := t.TempDir()
	content := `
policies:
`
	err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644)
	require.NoError(t, err)

	cfg, err := tracker.LoadPolicyConfig(dir)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestLoadPolicyConfig_BlockOnly(t *testing.T) {
	dir := t.TempDir()
	content := `
policies:
  block:
    - delete
    - create
`
	err := os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644)
	require.NoError(t, err)

	cfg, err := tracker.LoadPolicyConfig(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, []string{"delete", "create"}, cfg.Block)
	assert.Empty(t, cfg.Confirm)
}

func TestLoadPolicyConfig_NoConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := tracker.LoadPolicyConfig(dir)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}
