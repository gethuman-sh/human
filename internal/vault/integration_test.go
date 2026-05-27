package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/config"
)

// testVaultConfig is a minimal config struct for integration testing.
type testVaultConfig struct {
	Name  string `mapstructure:"name"`
	URL   string `mapstructure:"url"`
	Token string `mapstructure:"token"`
}

type testVaultInstance struct {
	Name  string
	URL   string
	Token string
}

func TestLoadInstances_withVaultResolver(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".humanconfig.yaml"),
		[]byte("vault:\n  provider: 1password\ntests:\n  - name: work\n    url: https://example.com\n    token: 1pw://vault/item/field\n"),
		0o644,
	))

	// Use a fake resolver instead of real 1Password.
	resolver := func(ref string) (string, error) {
		if ref == "1pw://vault/item/field" {
			return "resolved-from-vault", nil
		}
		return ref, nil
	}

	spec := config.InstanceSpec[testVaultConfig, testVaultInstance]{
		Section:   "tests",
		EnvPrefix: "TEST_VAULT_",
		EnvFields: []config.EnvField[testVaultConfig]{
			{Suffix: "URL", Set: func(c *testVaultConfig, v string) { c.URL = v }, Get: func(c testVaultConfig) string { return c.URL }},
			{Suffix: "TOKEN", Set: func(c *testVaultConfig, v string) { c.Token = v }, Get: func(c testVaultConfig) string { return c.Token }},
		},
		GetName:        func(c testVaultConfig) string { return c.Name },
		SetURL:         func(c *testVaultConfig, v string) { c.URL = v },
		GetURL:         func(c testVaultConfig) string { return c.URL },
		SecretResolver: resolver,
		Build: func(cfg testVaultConfig) (testVaultInstance, bool) {
			if cfg.Token == "" {
				return testVaultInstance{}, false
			}
			return testVaultInstance(cfg), true
		},
	}

	instances, err := config.LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "resolved-from-vault", instances[0].Token)
	assert.Equal(t, "https://example.com", instances[0].URL)
}

func TestLoadInstances_vaultAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".humanconfig.yaml"),
		[]byte("tests:\n  - name: work\n    url: https://example.com\n    token: 1pw://vault/item/field\n"),
		0o644,
	))

	// Env override takes precedence over YAML value, but vault resolution
	// still runs on the final value. If the env var itself contains a vault
	// reference, it gets resolved.
	t.Setenv("TEST_VAULT_WORK_TOKEN", "1pw://vault/env-item/field")

	resolver := func(ref string) (string, error) {
		switch ref {
		case "1pw://vault/item/field":
			return "yaml-vault-value", nil
		case "1pw://vault/env-item/field":
			return "env-vault-value", nil
		}
		return ref, nil
	}

	spec := config.InstanceSpec[testVaultConfig, testVaultInstance]{
		Section:   "tests",
		EnvPrefix: "TEST_VAULT_",
		EnvFields: []config.EnvField[testVaultConfig]{
			{Suffix: "URL", Set: func(c *testVaultConfig, v string) { c.URL = v }, Get: func(c testVaultConfig) string { return c.URL }},
			{Suffix: "TOKEN", Set: func(c *testVaultConfig, v string) { c.Token = v }, Get: func(c testVaultConfig) string { return c.Token }},
		},
		GetName:        func(c testVaultConfig) string { return c.Name },
		SetURL:         func(c *testVaultConfig, v string) { c.URL = v },
		GetURL:         func(c testVaultConfig) string { return c.URL },
		SecretResolver: resolver,
		Build: func(cfg testVaultConfig) (testVaultInstance, bool) {
			if cfg.Token == "" {
				return testVaultInstance{}, false
			}
			return testVaultInstance(cfg), true
		},
	}

	instances, err := config.LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	// Env override replaced the yaml value with 1pw://vault/env-item/field,
	// which then got resolved through the vault.
	assert.Equal(t, "env-vault-value", instances[0].Token)
}

func TestReadConfig_andNewResolver(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".humanconfig.yaml"),
		[]byte("vault:\n  provider: 1password\n  account: test-account\n"),
		0o644,
	))

	cfg, err := ReadConfig(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "1password", cfg.Provider)

	resolver := NewResolverFromConfig(cfg)
	require.NotNil(t, resolver)
	// The resolver has a 1Password provider.
	assert.Len(t, resolver.providers, 1)
}

func TestResolver_concurrentAccess(t *testing.T) {
	provider := &fakeProvider{
		canResolve: func(ref string) bool { return true },
		resolve: func(ref string) (string, error) {
			return "concurrent-value", nil
		},
	}
	r := NewResolver(provider)

	// Run concurrent resolve calls to verify thread safety.
	done := make(chan bool, 10)
	for i := range 10 {
		go func(idx int) {
			val, err := r.Resolve("1pw://vault/item/field")
			assert.NoError(t, err)
			assert.Equal(t, "concurrent-value", val)
			done <- true
		}(i)
	}
	for range 10 {
		<-done
	}
}
