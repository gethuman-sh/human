package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testConfig struct {
	Name  string `mapstructure:"name"`
	URL   string `mapstructure:"url"`
	Token string `mapstructure:"token"`
}

type testInstance struct {
	Name  string
	URL   string
	Token string
}

var testFields = []EnvField[testConfig]{
	{Suffix: "URL", Set: func(c *testConfig, v string) { c.URL = v }, Get: func(c testConfig) string { return c.URL }},
	{Suffix: "TOKEN", Set: func(c *testConfig, v string) { c.Token = v }, Get: func(c testConfig) string { return c.Token }},
}

func TestApplyEnvOverrides_instanceAndGlobal(t *testing.T) {
	t.Setenv("TEST_URL", "")
	t.Setenv("TEST_TOKEN", "")
	require.NoError(t, os.Unsetenv("TEST_URL"))
	require.NoError(t, os.Unsetenv("TEST_TOKEN"))

	t.Setenv("TEST_WORK_TOKEN", "instance-token")
	t.Setenv("TEST_TOKEN", "global-token")

	cfg := testConfig{Name: "work", URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, cfg.Name, "TEST_", testFields, nil)

	// Instance-specific takes precedence over global.
	assert.Equal(t, "instance-token", cfg.Token)
	assert.Equal(t, "file-url", cfg.URL)
}

func TestApplyEnvOverrides_instanceOnly(t *testing.T) {
	t.Setenv("TEST_URL", "")
	t.Setenv("TEST_TOKEN", "")
	require.NoError(t, os.Unsetenv("TEST_URL"))
	require.NoError(t, os.Unsetenv("TEST_TOKEN"))

	t.Setenv("TEST_WORK_TOKEN", "instance-token")
	t.Setenv("TEST_WORK_URL", "")
	require.NoError(t, os.Unsetenv("TEST_WORK_URL"))

	cfg := testConfig{Name: "work", URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, cfg.Name, "TEST_", testFields, nil)

	assert.Equal(t, "instance-token", cfg.Token)
	assert.Equal(t, "file-url", cfg.URL)
}

func TestApplyEnvOverrides_emptyName(t *testing.T) {
	t.Setenv("TEST_URL", "")
	t.Setenv("TEST_TOKEN", "")
	require.NoError(t, os.Unsetenv("TEST_URL"))
	require.NoError(t, os.Unsetenv("TEST_TOKEN"))

	cfg := testConfig{URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, "", "TEST_", testFields, nil)

	// No instance prefix, no global set → unchanged.
	assert.Equal(t, "file-url", cfg.URL)
	assert.Equal(t, "file-token", cfg.Token)
}

func TestApplyEnvOverrides_globalOnly(t *testing.T) {
	t.Setenv("TEST_URL", "global-url")
	t.Setenv("TEST_TOKEN", "")
	require.NoError(t, os.Unsetenv("TEST_TOKEN"))

	cfg := testConfig{Name: "work", URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, cfg.Name, "TEST_", testFields, nil)

	assert.Equal(t, "global-url", cfg.URL)
	assert.Equal(t, "file-token", cfg.Token)
}

func writeTestConfig(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(content), 0o644))
}

func testSpec(defaultURL string) InstanceSpec[testConfig, testInstance] {
	return InstanceSpec[testConfig, testInstance]{
		Section:    "tests",
		EnvPrefix:  "TEST_",
		DefaultURL: defaultURL,
		EnvFields:  testFields,
		GetName:    func(c testConfig) string { return c.Name },
		SetURL:     func(c *testConfig, v string) { c.URL = v },
		GetURL:     func(c testConfig) string { return c.URL },
		Build: func(cfg testConfig) (testInstance, bool) {
			if cfg.Token == "" {
				return testInstance{}, false
			}
			return testInstance(cfg), true
		},
	}
}

func unsetTestEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"TEST_URL", "TEST_TOKEN", "TEST_WORK_URL", "TEST_WORK_TOKEN"} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

func TestLoadInstances_happyPath(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: tok\n")
	unsetTestEnv(t)

	instances, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "work", instances[0].Name)
	assert.Equal(t, "https://example.com", instances[0].URL)
	assert.Equal(t, "tok", instances[0].Token)
}

func TestLoadInstances_defaultURL(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    token: tok\n")
	unsetTestEnv(t)

	instances, err := LoadInstances(dir, testSpec("https://default.example.com"))
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "https://default.example.com", instances[0].URL)
}

func TestLoadInstances_incompleteSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n")
	unsetTestEnv(t)

	instances, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	assert.Empty(t, instances)
}

// captureLog redirects the global zerolog logger to a buffer for the duration
// of the test, restoring the previous logger afterwards.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })
	return &buf
}

func TestLoadInstances_warnsOnSkippedInstance(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n")
	unsetTestEnv(t)

	buf := captureLog(t)

	instances, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	assert.Empty(t, instances)

	out := buf.String()
	assert.Contains(t, out, "work")
	assert.Contains(t, out, "TEST_WORK_TOKEN")
	assert.Contains(t, out, "TEST_TOKEN")
	assert.Contains(t, out, "skipped")
}

func TestLoadInstances_noWarnOnValidInstance(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: tok\n")
	unsetTestEnv(t)

	buf := captureLog(t)

	instances, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	require.Len(t, instances, 1)

	assert.NotContains(t, buf.String(), "skipped")
}

func TestLoadInstances_envOverride(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: file-tok\n")
	unsetTestEnv(t)
	t.Setenv("TEST_TOKEN", "global-tok")

	instances, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "global-tok", instances[0].Token)
}

func TestLoadInstances_missingFile(t *testing.T) {
	dir := t.TempDir()
	instances, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestLoadInstances_noURLCallbacks(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    token: tok\n")
	unsetTestEnv(t)

	// Spec with no URL callbacks (like Telegram).
	spec := InstanceSpec[testConfig, testInstance]{
		Section:   "tests",
		EnvPrefix: "TEST_",
		EnvFields: testFields,
		GetName:   func(c testConfig) string { return c.Name },
		Build: func(cfg testConfig) (testInstance, bool) {
			if cfg.Token == "" {
				return testInstance{}, false
			}
			return testInstance{Name: cfg.Name, Token: cfg.Token}, true
		},
	}

	instances, err := LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "work", instances[0].Name)
}

func TestApplyEnvOverrides_customLookup(t *testing.T) {
	// Custom lookup always returns project-scoped values.
	lookup := func(key string) (string, bool) {
		switch key {
		case "TEST_TOKEN":
			return "custom-global-tok", true
		case "TEST_WORK_TOKEN":
			return "custom-instance-tok", true
		default:
			return "", false
		}
	}

	cfg := testConfig{Name: "work", URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, cfg.Name, "TEST_", testFields, lookup)

	// Instance-specific from custom lookup takes precedence.
	assert.Equal(t, "custom-instance-tok", cfg.Token)
	assert.Equal(t, "file-url", cfg.URL)
}

func TestApplyEnvOverrides_customLookup_globalOnly(t *testing.T) {
	// Custom lookup returns only global, not instance-specific.
	lookup := func(key string) (string, bool) {
		if key == "TEST_TOKEN" {
			return "custom-global-tok", true
		}
		return "", false
	}

	cfg := testConfig{Name: "work", URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, cfg.Name, "TEST_", testFields, lookup)

	// Global from custom lookup applies.
	assert.Equal(t, "custom-global-tok", cfg.Token)
	assert.Equal(t, "file-url", cfg.URL)
}

func TestApplyEnvOverrides_customLookup_noMatch(t *testing.T) {
	// Custom lookup never finds anything.
	lookup := func(_ string) (string, bool) {
		return "", false
	}

	cfg := testConfig{Name: "work", URL: "file-url", Token: "file-token"}
	ApplyEnvOverrides(&cfg, cfg.Name, "TEST_", testFields, lookup)

	// Values unchanged.
	assert.Equal(t, "file-url", cfg.URL)
	assert.Equal(t, "file-token", cfg.Token)
}

func TestLoadInstances_withLookup(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n")
	unsetTestEnv(t)

	// Custom lookup provides token via project-scoped env.
	lookup := func(key string) (string, bool) {
		if key == "TEST_TOKEN" {
			return "scoped-tok", true
		}
		return "", false
	}

	spec := testSpec("")
	spec.Lookup = lookup

	instances, err := LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "scoped-tok", instances[0].Token)
}

func TestLoadInstances_lookupOverridesOsEnv(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n")
	unsetTestEnv(t)
	t.Setenv("TEST_TOKEN", "os-env-tok")

	// Custom lookup takes precedence over os env (since it replaces os.LookupEnv entirely).
	lookup := func(key string) (string, bool) {
		if key == "TEST_TOKEN" {
			return "lookup-tok", true
		}
		return os.LookupEnv(key)
	}

	spec := testSpec("")
	spec.Lookup = lookup

	instances, err := LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "lookup-tok", instances[0].Token)
}

func TestLoadInstances_secretResolver(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: 1pw://vault/item/field\n")
	unsetTestEnv(t)

	resolver := func(ref string) (string, error) {
		if ref == "1pw://vault/item/field" {
			return "resolved-secret", nil
		}
		return ref, nil
	}

	spec := testSpec("")
	spec.SecretResolver = resolver

	instances, err := LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "resolved-secret", instances[0].Token)
}

func TestLoadInstances_secretResolverError(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: 1pw://vault/item/field\n")
	unsetTestEnv(t)

	resolver := func(ref string) (string, error) {
		return "", assert.AnError
	}

	spec := testSpec("")
	spec.SecretResolver = resolver

	_, err := LoadInstances(dir, spec)
	require.Error(t, err)
}

func TestLoadInstances_secretResolverNil(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: 1pw://vault/item/field\n")
	unsetTestEnv(t)

	// No resolver — vault refs stay as literal strings.
	spec := testSpec("")
	instances, err := LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "1pw://vault/item/field", instances[0].Token)
}

func TestLoadInstances_secretResolverPlainValueUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: plain-tok\n")
	unsetTestEnv(t)

	calls := 0
	resolver := func(ref string) (string, error) {
		calls++
		return ref, nil // pass-through
	}

	spec := testSpec("")
	spec.SecretResolver = resolver

	instances, err := LoadInstances(dir, spec)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "plain-tok", instances[0].Token)
	assert.Equal(t, 2, calls) // called for both URL and Token
}

func TestResolveSecrets_fieldsWithoutGet(t *testing.T) {
	// Fields without Get are silently skipped.
	fieldsNoGet := []EnvField[testConfig]{
		{Suffix: "TOKEN", Set: func(c *testConfig, v string) { c.Token = v }},
	}

	cfg := testConfig{Token: "1pw://vault/item/field"}
	err := resolveSecrets(&cfg, fieldsNoGet, func(ref string) (string, error) {
		return "should-not-be-called", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "1pw://vault/item/field", cfg.Token) // unchanged
}
