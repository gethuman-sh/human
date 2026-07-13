package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
)

func readBack(t *testing.T, dir string) string {
	t.Helper()
	file, ok := LocateConfigFile(dir)
	require.True(t, ok, "config file must exist after write")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	return string(data)
}

func TestSetValuePreservesCommentsAndOrder(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	require.NoError(t, SetValue(dir, "linears.work.projects", []string{"HUM", "OPS"}))

	out := readBack(t, dir)
	// Comments survive the node round-trip.
	assert.Contains(t, out, "# team config — do not commit tokens")
	assert.Contains(t, out, "# PM tickets live here")
	// Key order survives: project before vault before linears before shortcuts.
	assert.Less(t, strings.Index(out, "project:"), strings.Index(out, "vault:"))
	assert.Less(t, strings.Index(out, "vault:"), strings.Index(out, "linears:"))
	assert.Less(t, strings.Index(out, "linears:"), strings.Index(out, "shortcuts:"))
	// Untouched values survive verbatim.
	assert.Contains(t, out, "sc-plaintext-secret")

	// Viper agrees with what yaml.v3 wrote.
	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "linears", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, []any{"HUM", "OPS"}, entries[0]["projects"])
}

func TestSetValueScalarInExistingEntry(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	require.NoError(t, SetValue(dir, "linears.work.description", "engineering tickets"))
	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "linears", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "engineering tickets", entries[0]["description"])
}

func TestSetValueBoolAndEnum(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	require.NoError(t, SetValue(dir, "linears.work.safe", true))
	require.NoError(t, SetValue(dir, "linears.work.role", "pm"))
	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "linears", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, true, entries[0]["safe"])
	assert.Equal(t, "pm", entries[0]["role"])
}

func TestSetValueAppendsFieldToSparseEntry(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", `gitlabs:
  - name: human
`)
	require.NoError(t, SetValue(dir, "gitlabs.human.token", "1pw://Development/Gitlab Token/token"))
	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "gitlabs", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "1pw://Development/Gitlab Token/token", entries[0]["token"])
	assert.Equal(t, "human", entries[0]["name"])
}

func TestSetValueCreatesSectionAndInstance(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", "project: human-cli\n")
	require.NoError(t, SetValue(dir, "linears.work.token", "1pw://a/b/c"))
	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "linears", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "work", entries[0]["name"])
	assert.Equal(t, "1pw://a/b/c", entries[0]["token"])
	assert.Equal(t, "human-cli", config.ReadProjectName(dir))
}

func TestSetValueCreatesFileFromNothing(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, SetValue(dir, "project", "fresh"))
	file, ok := LocateConfigFile(dir)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, ".humanconfig.yaml"), file)
	assert.Equal(t, "fresh", config.ReadProjectName(dir))
}

func TestSetValueSingletonSection(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	require.NoError(t, SetValue(dir, "vault.account", "newaccount"))
	var m map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "vault", &m))
	assert.Equal(t, "newaccount", m["account"])
	assert.Equal(t, "1password", m["provider"])
}

func TestSetValueEditsExtensionlessInPlace(t *testing.T) {
	dir := writeFixture(t, ".humanconfig", fixtureConfig)
	require.NoError(t, SetValue(dir, "vault.account", "changed"))
	// No new .yaml appears; the extensionless file is the one edited.
	_, err := os.Stat(filepath.Join(dir, ".humanconfig.yaml"))
	assert.True(t, os.IsNotExist(err))
	out := readBack(t, dir)
	assert.Contains(t, out, "changed")
}

func TestSetValueEditsLocalVariantInPlace(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local", ".humanconfig.yaml"), []byte(fixtureConfig), 0o644))
	require.NoError(t, SetValue(dir, "vault.account", "localedit"))
	_, err := os.Stat(filepath.Join(dir, ".humanconfig.yaml"))
	assert.True(t, os.IsNotExist(err), "root file must not be created")
	data, err := os.ReadFile(filepath.Join(dir, "local", ".humanconfig.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "localedit")
}

func TestSetValueUnknownSectionsSurvive(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", `project: human-cli
somefuturesection:
  nested:
    - a
    - b
`)
	require.NoError(t, SetValue(dir, "project", "renamed"))
	out := readBack(t, dir)
	assert.Contains(t, out, "somefuturesection:")
	assert.Contains(t, out, "- a")
}

func TestSetValueIntListAndInt(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", "telegrams:\n  - name: bot\n")
	// JSON decoding hands numbers over as float64 — the writer must accept
	// integral floats and reject fractions.
	require.NoError(t, SetValue(dir, "telegrams.bot.allowed_users", []any{float64(123), float64(456)}))
	require.NoError(t, SetValue(dir, "telegrams.bot.notify_chat_id", float64(-100200)))
	assert.Error(t, SetValue(dir, "telegrams.bot.notify_chat_id", 1.5))

	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "telegrams", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, []any{123, 456}, entries[0]["allowed_users"])
	assert.Equal(t, -100200, entries[0]["notify_chat_id"])
}

func TestSetValueRejectsMaskedSentinel(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	err := SetValue(dir, "shortcuts.human.token", Masked)
	require.Error(t, err)
	assert.Contains(t, errors.CauseChain(err), "masked")
	// File untouched.
	assert.Contains(t, readBack(t, dir), "sc-plaintext-secret")
}

func TestSetValueRejectsDuplicateName(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", `linears:
  - name: work
  - name: work
`)
	err := SetValue(dir, "linears.work.token", "x")
	require.Error(t, err)
	assert.Contains(t, errors.CauseChain(err), "duplicate")
}

func TestSetValueRejectsWrongTypes(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	assert.Error(t, SetValue(dir, "linears.work.safe", "yes"))
	assert.Error(t, SetValue(dir, "linears.work.role", "boss"))
	assert.Error(t, SetValue(dir, "linears.work.projects", "HUM"))
	assert.Error(t, SetValue(dir, "vault.provider", "keepass"))
	assert.Error(t, SetValue(dir, "project", 42))
}

func TestSetValueIndexAddressing(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", `linears:
  - token: unnamed-first
`)
	require.NoError(t, SetValue(dir, "linears[0].token", "1pw://x/y/z"))
	var entries []map[string]any
	require.NoError(t, config.UnmarshalSection(dir, "linears", &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "1pw://x/y/z", entries[0]["token"])

	assert.Error(t, SetValue(dir, "linears[5].token", "x"), "out-of-range index")
}

func TestSetValuePreservesInlineCommentOnEditedValue(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", "project: old # keep me\n")
	require.NoError(t, SetValue(dir, "project", "new"))
	out := readBack(t, dir)
	assert.Contains(t, out, "# keep me")
	assert.Contains(t, out, "new")
}

func TestSetValueRootNotMapping(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", "- just\n- a list\n")
	assert.Error(t, SetValue(dir, "project", "x"))
}

func TestLocateConfigFileOrder(t *testing.T) {
	dir := t.TempDir()
	_, ok := LocateConfigFile(dir)
	assert.False(t, ok)

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig"), []byte("a: 1\n"), 0o644))
	file, ok := LocateConfigFile(dir)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, ".humanconfig"), file)

	// The .yaml variant wins over extensionless once both exist (viper
	// checks extensions first) — this repo itself has both.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("a: 1\n"), 0o644))
	file, ok = LocateConfigFile(dir)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, ".humanconfig.yaml"), file)
}

func TestSetValuePreservesFilePermissions(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, ".humanconfig.yaml")
	require.NoError(t, os.WriteFile(file, []byte("project: x\n"), 0o600))
	require.NoError(t, SetValue(dir, "project", "y"))
	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
