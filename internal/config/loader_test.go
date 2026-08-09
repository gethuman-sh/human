package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
//
// It also clears the record of already-reported skips. That record is
// process-global by design (it has to outlive a single config rebuild), so
// without this an earlier test that skipped the same entry would suppress the
// warning this test is asserting on.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	forgetAllSkippedInstances()
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })
	return &buf
}

// forgetAllSkippedInstances resets the skip memory so each test starts from a
// clean slate.
func forgetAllSkippedInstances() {
	skippedInstances.mu.Lock()
	defer skippedInstances.mu.Unlock()
	skippedInstances.reported = map[string]string{}
}

// countWarnings reports how many log lines mention a skipped instance.
func countWarnings(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "skipped configured instance")
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

// A missing credential cannot appear between two rebuilds, and the daemon
// rebuilds several times a second — so the warning must be said once, not once
// per rebuild (SC-2605).
func TestLoadInstances_warnsOnceWhileReasonUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n")
	unsetTestEnv(t)

	buf := captureLog(t)

	for range 5 {
		instances, err := LoadInstances(dir, testSpec(""))
		require.NoError(t, err)
		require.Empty(t, instances)
	}

	assert.Equal(t, 1, countWarnings(buf), "an unchanged condition is news only the first time")
}

// Losing a different credential is a different state, so it is worth saying
// again rather than being swallowed as a repeat.
func TestLoadInstances_warnsAgainWhenReasonChanges(t *testing.T) {
	unsetTestEnv(t)
	buf := captureLog(t)

	// Build always refuses, so the reason is whichever fields resolve empty.
	spec := testSpec("")
	spec.Build = func(testConfig) (testInstance, bool) { return testInstance{}, false }

	missingToken := t.TempDir()
	writeTestConfig(t, missingToken, "tests:\n  - name: work\n    url: https://example.com\n")
	missingURL := t.TempDir()
	writeTestConfig(t, missingURL, "tests:\n  - name: work\n    token: tok\n")

	for _, dir := range []string{missingToken, missingToken, missingURL, missingURL} {
		_, err := LoadInstances(dir, spec)
		require.NoError(t, err)
	}

	assert.Equal(t, 2, countWarnings(buf), "one line per distinct reason, not per rebuild")
}

// A credential that comes back and goes missing again has to be reported
// afresh — the second outage is not a repeat of the first.
func TestLoadInstances_warnsAgainAfterInstanceRecovers(t *testing.T) {
	unsetTestEnv(t)
	buf := captureLog(t)

	broken := t.TempDir()
	writeTestConfig(t, broken, "tests:\n  - name: work\n    url: https://example.com\n")
	healthy := t.TempDir()
	writeTestConfig(t, healthy, "tests:\n  - name: work\n    url: https://example.com\n    token: tok\n")

	for _, dir := range []string{broken, healthy, broken} {
		_, err := LoadInstances(dir, testSpec(""))
		require.NoError(t, err)
	}

	assert.Equal(t, 2, countWarnings(buf), "the outage after a recovery is new news")
}

// Two sections may each configure an instance called "work"; one being skipped
// must not silence the other.
func TestLoadInstances_warnsPerSectionForSameInstanceName(t *testing.T) {
	unsetTestEnv(t)
	buf := captureLog(t)

	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n")

	first := testSpec("")
	second := testSpec("")
	second.Section = "others"

	for _, spec := range []InstanceSpec[testConfig, testInstance]{first, second} {
		_, err := LoadInstances(dir, spec)
		require.NoError(t, err)
	}

	// Only "tests" is present in the file, so "others" loads nothing and warns
	// nothing; the point is that the key is not the bare instance name.
	assert.Equal(t, 1, countWarnings(buf))
	assert.NotEqual(t, skippedInstanceKey("tests", "work"), skippedInstanceKey("others", "work"))
}

// An entry rejected for a reason not tied to an empty credential field takes
// the other branch, which must dedupe too.
func TestLoadInstances_warnsOnceForIncompleteConfig(t *testing.T) {
	unsetTestEnv(t)
	buf := captureLog(t)

	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: work\n    url: https://example.com\n    token: tok\n")

	spec := testSpec("")
	spec.EnvFields = nil // nothing resolves empty, so no field explains the skip
	spec.Build = func(testConfig) (testInstance, bool) { return testInstance{}, false }

	for range 3 {
		_, err := LoadInstances(dir, spec)
		require.NoError(t, err)
	}

	out := buf.String()
	assert.Equal(t, 1, countWarnings(buf))
	assert.Contains(t, out, "required configuration is incomplete")
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

// --- Unified trackers: section (SC-3874) ---

// unifiedSpec is testSpec addressable from the trackers: section.
func unifiedSpec() InstanceSpec[testConfig, testInstance] {
	spec := testSpec("")
	spec.Kind = "test"
	return spec
}

// The same backend, declared the new way: the section says what it is and the
// vendor is a field.
func TestLoadInstances_readsTheUnifiedSection(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "trackers:\n  - kind: test\n    name: work\n    token: tok\n")
	unsetTestEnv(t)

	got, err := LoadInstances(dir, unifiedSpec())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "work", got[0].Name)
}

// An entry of another kind in the same list is not this provider's business.
func TestLoadInstances_ignoresOtherKinds(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "trackers:\n  - kind: other\n    name: nope\n    token: tok\n  - kind: test\n    name: mine\n    token: tok\n")
	unsetTestEnv(t)

	got, err := LoadInstances(dir, unifiedSpec())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "mine", got[0].Name)
}

// Both shapes at once: a config part-way through a migration, or one keeping a
// legacy entry deliberately, must not lose either half.
func TestLoadInstances_readsBothShapes(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "tests:\n  - name: legacy\n    token: tok\ntrackers:\n  - kind: test\n    name: unified\n    token: tok\n")
	unsetTestEnv(t)

	got, err := LoadInstances(dir, unifiedSpec())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "legacy", got[0].Name, "the legacy section is read first, so its entries keep their order")
	assert.Equal(t, "unified", got[1].Name)
}

// An entry with no kind belongs to nobody: it is a typo, not every provider's
// entry at once.
func TestLoadInstances_unifiedEntryNeedsAKind(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "trackers:\n  - name: work\n    token: tok\n")
	unsetTestEnv(t)

	got, err := LoadInstances(dir, unifiedSpec())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A spec with no kind is not addressable from the unified section at all —
// forges: is already grouped by capability and has no vendor list to merge.
func TestLoadInstances_specWithoutKindIgnoresTheUnifiedSection(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "trackers:\n  - kind: test\n    name: work\n    token: tok\n")
	unsetTestEnv(t)

	got, err := LoadInstances(dir, testSpec(""))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Tokens keep their names: a config that changes shape must not change what its
// environment variables are called, or every install breaks on a rename.
func TestLoadInstances_unifiedEntryKeepsEnvNaming(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, dir, "trackers:\n  - kind: test\n    name: work\n")
	unsetTestEnv(t)
	t.Setenv("TEST_WORK_TOKEN", "from-env")

	got, err := LoadInstances(dir, unifiedSpec())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "from-env", got[0].Token)
}
