package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/tracker"
)

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(yaml), 0o600))
	t.Cleanup(resetShared)
	return dir
}

func TestLoadInstances_unifiedEntryWithDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := writeConfig(t, "project: demo\nme:\n  names:\n    - Ada Lovelace\ntrackers:\n  - kind: local\n    name: tickets\n")

	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	inst := instances[0]
	assert.Equal(t, "tickets", inst.Name)
	assert.Equal(t, "local", inst.Kind)
	assert.Equal(t, []string{"LOC"}, inst.Projects)
	assert.Equal(t, "LOC", inst.FilingTarget(), "the prefix is the filing target so captures land on the board")
	assert.Equal(t, "Ada Lovelace", inst.User, "writes are attributed to the first me: name")
	assert.True(t, strings.HasPrefix(inst.URL, filepath.Join(os.Getenv("HOME"), ".human", "local")), inst.URL)
	assert.True(t, strings.HasPrefix(filepath.Base(inst.URL), "demo-tickets-"), inst.URL)

	issue, err := inst.Provider.CreateIssue(context.Background(), &tracker.Issue{Title: "first"})
	require.NoError(t, err)
	assert.Equal(t, "LOC-1", issue.Key)
	assert.Equal(t, "Ada Lovelace", issue.Reporter)
	_, statErr := os.Stat(inst.URL)
	assert.NoError(t, statErr, "the database is created at load, not at first write")
}

func TestLoadInstances_legacySectionPrefixPathAndUser(t *testing.T) {
	dir := writeConfig(t, "locals:\n  - name: mine\n    prefix: hum\n    path: tickets/local.db\n    user: bot\n    role: pm\n    safe: true\n    description: try-out\n")

	instances, err := LoadInstancesWithResolver(dir, nil, nil)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	inst := instances[0]
	assert.Equal(t, filepath.Join(dir, "tickets", "local.db"), inst.URL, "a relative path is taken from the project")
	assert.Equal(t, []string{"HUM"}, inst.Projects, "the prefix is upper-cased")
	assert.Equal(t, "bot", inst.User)
	assert.Equal(t, "pm", inst.Role)
	assert.True(t, inst.Safe)
	assert.Equal(t, "try-out", inst.Description)

	issue, err := inst.Provider.CreateIssue(context.Background(), &tracker.Issue{Title: "x"})
	require.NoError(t, err)
	assert.Equal(t, "HUM-1", issue.Key)
}

func TestLoadInstances_badPrefixIsSkippedNotFatal(t *testing.T) {
	dir := writeConfig(t, "trackers:\n  - kind: local\n    name: bad\n    prefix: sc\n    path: x.db\n")
	instances, err := LoadInstances(dir)
	require.NoError(t, err)
	assert.Empty(t, instances, "an entry that cannot mint routable keys is skipped like a tracker with no token")
}

func TestLoadInstances_noEntriesNoInstances(t *testing.T) {
	dir := writeConfig(t, "trackers:\n  - kind: jira\n    name: work\n    url: https://x\n")
	instances, err := LoadInstancesWithLookup(dir, nil)
	require.NoError(t, err)
	assert.Empty(t, instances)
}

func TestSharedClient_reusedAcrossLoads_refusesSecondPrefix(t *testing.T) {
	dir := writeConfig(t, "trackers:\n  - kind: local\n    name: a\n    path: shared.db\n")
	first, err := LoadInstances(dir)
	require.NoError(t, err)
	second, err := LoadInstances(dir)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	assert.Same(t, first[0].Provider, second[0].Provider, "every load reuses the one open handle")

	_, err = sharedClient(filepath.Join(dir, "shared.db"), "OTHER", "x", false)
	assert.Error(t, err, "the same file under another prefix would relabel every key in it")
}

func TestDefaultDBPath_distinguishesCheckoutsOfOneProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := t.TempDir()
	b := t.TempDir()
	pa, err := DefaultDBPath(a, "tickets")
	require.NoError(t, err)
	pb, err := DefaultDBPath(b, "tickets")
	require.NoError(t, err)
	assert.NotEqual(t, pa, pb)
	assert.Equal(t, filepath.Dir(pa), filepath.Dir(pb))
	assert.True(t, strings.HasSuffix(pa, ".db"))
}

func TestResolveUser_fallsBackToOSUser(t *testing.T) {
	dir := t.TempDir()
	got := resolveUser(dir, Config{})
	assert.NotEmpty(t, got)
	assert.Equal(t, "explicit", resolveUser(dir, Config{User: " explicit "}))
}

func TestSlug(t *testing.T) {
	assert.Equal(t, "my-project", slug(" My Project "))
	assert.Equal(t, "a.b_c-d", slug("a.b_c-d"))
	assert.Equal(t, "", slug("///"))
}
