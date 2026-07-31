package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644))
}

func TestReadConfig_1password(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "vault:\n  provider: 1password\n")

	cfg, err := ReadConfig(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "1password", cfg.Provider)
}

func TestReadConfig_noVaultSection(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "githubs:\n  - name: personal\n    token: tok\n")

	cfg, err := ReadConfig(dir)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestReadConfig_emptyProvider(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "vault:\n  provider: \"\"\n")

	cfg, err := ReadConfig(dir)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestReadConfig_missingFile(t *testing.T) {
	dir := t.TempDir()

	cfg, err := ReadConfig(dir)
	require.NoError(t, err)
	assert.Nil(t, cfg)
}

func TestReadConfig_parseErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	// Deliberately malformed YAML — the scanner will fail.
	writeConfig(t, dir, "vault:\n  provider: [not a string\n")

	cfg, err := ReadConfig(dir)
	require.Error(t, err)
	assert.Nil(t, cfg)
}

func TestReadConfig_cacheTTLValid(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "vault:\n  provider: 1password\n  cache_ttl: 5m\n")

	cfg, err := ReadConfig(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "5m", cfg.CacheTTL)
	assert.Equal(t, 5*time.Minute, cfg.cacheTTL())
}

func TestReadConfig_cacheTTLInvalidErrors(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "vault:\n  provider: 1password\n  cache_ttl: notaduration\n")

	cfg, err := ReadConfig(dir)
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "cache_ttl")
}

func TestConfig_cacheTTLDefaultsWhenUnset(t *testing.T) {
	cfg := &Config{Provider: "1password"}
	assert.Equal(t, DefaultCacheTTL, cfg.cacheTTL())
}

func TestNewResolverFromConfig_appliesCacheTTL(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "1password", CacheTTL: "1m"})
	require.NotNil(t, r)
	assert.Equal(t, time.Minute, r.ttl)
}

func TestNewResolverFromConfig_defaultCacheTTL(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "github"})
	require.NotNil(t, r)
	assert.Equal(t, DefaultCacheTTL, r.ttl)
}

func TestNewResolverFromConfig_nil(t *testing.T) {
	r := NewResolverFromConfig(nil)
	assert.Nil(t, r)
}

func TestNewResolverFromConfig_1password(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "1password"})
	require.NotNil(t, r)
	assert.Len(t, r.providers, 2) // op CLI + GitHub CLI
}

func TestNewResolverFromConfig_1pw(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "1pw"})
	require.NotNil(t, r)
	assert.Len(t, r.providers, 2) // op CLI + GitHub CLI
}

func TestNewResolverFromConfig_unknownProvider(t *testing.T) {
	r := NewResolverFromConfig(&Config{Provider: "unknown"})
	assert.Nil(t, r)
}
