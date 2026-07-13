package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixtureConfig = `# team config — do not commit tokens
project: human-cli

vault:
  provider: 1password
  account: amazingcto

linears:
  - name: work
    role: engineering
    token: 1pw://Development/Linear Token/token
    projects: [HUM]

shortcuts:
  - name: human   # PM tickets live here
    role: pm
    token: sc-plaintext-secret
    safe: true
`

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	return dir
}

func valueByPath(t *testing.T, doc Doc, path string) Value {
	t.Helper()
	for _, v := range doc.Values {
		if v.Path == path {
			return v
		}
	}
	t.Fatalf("no value with path %q", path)
	return Value{}
}

func TestSnapshotMasksLiteralSecret(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	v := valueByPath(t, doc, "shortcuts.human.token")
	assert.Equal(t, Masked, v.Value)
	assert.True(t, v.Masked)
	assert.False(t, v.SecretRef)
}

func TestSnapshotKeepsVaultRefVerbatim(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	v := valueByPath(t, doc, "linears.work.token")
	assert.Equal(t, "1pw://Development/Linear Token/token", v.Value)
	assert.True(t, v.SecretRef)
	assert.False(t, v.Masked)
}

func TestSnapshotFlattensTypedValues(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", fixtureConfig)
	doc, err := Snapshot(dir)
	require.NoError(t, err)

	assert.Equal(t, "human-cli", valueByPath(t, doc, "project").Value)
	assert.Equal(t, "1password", valueByPath(t, doc, "vault.provider").Value)
	assert.Equal(t, []string{"HUM"}, valueByPath(t, doc, "linears.work.projects").Value)
	assert.Equal(t, true, valueByPath(t, doc, "shortcuts.human.safe").Value)
	assert.Equal(t, "engineering", valueByPath(t, doc, "linears.work.role").Value)
	assert.True(t, doc.Exists)
	assert.Equal(t, filepath.Join(dir, ".humanconfig.yaml"), doc.ConfigFile)
}

func TestSnapshotMissingFileYieldsSkeleton(t *testing.T) {
	doc, err := Snapshot(t.TempDir())
	require.NoError(t, err)
	assert.False(t, doc.Exists)
	assert.Empty(t, doc.ConfigFile)
	// Singleton and scalar leaves render as an editable skeleton.
	assert.Equal(t, "", valueByPath(t, doc, "project").Value)
	assert.Equal(t, "", valueByPath(t, doc, "vault.account").Value)
}

func TestSnapshotDuplicateNamesWarnAndReadOnly(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", `linears:
  - name: work
    token: 1pw://a/b/c
  - name: work
    token: other
`)
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	require.NotEmpty(t, doc.Warnings)
	assert.Contains(t, doc.Warnings[0], "duplicate")

	// The later duplicate is index-addressed and read-only.
	v := valueByPath(t, doc, "linears[1].token")
	assert.True(t, v.ReadOnly)
	assert.Equal(t, "work", v.Instance)
	first := valueByPath(t, doc, "linears.work.token")
	assert.False(t, first.ReadOnly)
}

func TestSnapshotExtensionlessConfigFound(t *testing.T) {
	dir := writeFixture(t, ".humanconfig", fixtureConfig)
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	assert.True(t, doc.Exists)
	assert.Equal(t, filepath.Join(dir, ".humanconfig"), doc.ConfigFile)
	assert.Equal(t, "human-cli", valueByPath(t, doc, "project").Value)
}

func TestSnapshotLocalSubdirConfigFound(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local", ".humanconfig.yaml"), []byte(fixtureConfig), 0o644))
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	assert.True(t, doc.Exists)
	assert.Equal(t, filepath.Join(dir, "local", ".humanconfig.yaml"), doc.ConfigFile)
}

func TestSnapshotRootWinsOverLocal(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", "project: root-project\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "local"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local", ".humanconfig.yaml"), []byte("project: local-project\n"), 0o644))
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".humanconfig.yaml"), doc.ConfigFile)
	assert.Equal(t, "root-project", valueByPath(t, doc, "project").Value)
}

func TestSnapshotTelegramIntLists(t *testing.T) {
	dir := writeFixture(t, ".humanconfig.yaml", `telegrams:
  - name: bot
    allowed_users: [12345, 67890]
    notify_chat_id: -100200
`)
	doc, err := Snapshot(dir)
	require.NoError(t, err)
	assert.Equal(t, []int64{12345, 67890}, valueByPath(t, doc, "telegrams.bot.allowed_users").Value)
	assert.Equal(t, int64(-100200), valueByPath(t, doc, "telegrams.bot.notify_chat_id").Value)
	// Absent list normalizes to an empty list, not nil, for the frontend.
	assert.Equal(t, []int64{}, valueByPath(t, doc, "telegrams.bot.allowed_chats").Value)
}
