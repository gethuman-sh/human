package cmddaemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/mockups"
)

// writeGroup writes a mockups/<slug>/index.json with the given parent link and
// one option file, so pruning/subtree logic has real dirs to walk.
func writeGroup(t *testing.T, projectDir, slug, parent, file string) {
	t.Helper()
	dir := filepath.Join(projectDir, "mockups", slug)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	manifest := childManifest{
		Slug:    slug,
		Parent:  parent,
		Created: "2026-07-24T00:00:00Z",
		Options: []json.RawMessage{},
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), data, 0o600))
	if file != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("<html></html>"), 0o600))
	}
}

// writeRealGroup writes a manifest whose "options" is populated with objects —
// the shape the /human-mockups skill actually writes
// (human-mockups-skill.md:66-78) — rather than the daemon's 0-option
// placeholder. This is what SC-4991's mount starts delivering into the project.
func writeRealGroup(t *testing.T, projectDir, slug, parent, file string) {
	t.Helper()
	dir := filepath.Join(projectDir, "mockups", slug)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	body := `{"slug":"` + slug + `","parent":"` + parent + `","created":"2026-07-24T00:00:00Z",` +
		`"options":[{"n":1,"name":"A","file":"` + file + `","description":"x"}]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), []byte(body), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("<html></html>"), 0o600))
}

func testRegistry(t *testing.T, dir string) *daemon.ProjectRegistry {
	t.Helper()
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	return reg
}

func TestLeadingDigits(t *testing.T) {
	assert.Equal(t, "03", leadingDigits("03-foo.html"))
	assert.Equal(t, "2", leadingDigits("2.html"))
	assert.Equal(t, "", leadingDigits("foo.html"))
	assert.Equal(t, "", leadingDigits(""))
}

func TestNextVariationSlug_SkipsExisting(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1", "", "01.html")
	writeGroup(t, dir, "sc-1-o3-v1", "sc-1", "01.html")

	got := nextVariationSlug(dir, "sc-1", "03-foo.html")
	assert.Equal(t, "sc-1-o3-v2", got)
}

func TestNextVariationSlug_NoLeadingDigits(t *testing.T) {
	dir := t.TempDir()
	got := nextVariationSlug(dir, "sc-1", "foo.html")
	assert.Equal(t, "sc-1-v1", got)
}

func TestNextVariationSlug_FirstFree(t *testing.T) {
	dir := t.TempDir()
	got := nextVariationSlug(dir, "sc-1", "02.html")
	assert.Equal(t, "sc-1-o2-v1", got)
}

func TestMockupChooser_RecordsAndValidates(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1-o3-v1", "sc-1", "02.html")
	choose := mockupChooserFunc(testRegistry(t, dir))

	require.NoError(t, choose(daemon.ChooseMockupRequest{PMKey: "SC-1", Slug: "sc-1-o3-v1", File: "02.html"}))

	store := mockups.NewStore(mockups.PathIn(dir))
	c, ok := store.ChosenFor("SC-1")
	assert.True(t, ok)
	assert.Equal(t, mockups.Choice{Slug: "sc-1-o3-v1", File: "02.html"}, c)
}

func TestMockupChooser_MissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	choose := mockupChooserFunc(testRegistry(t, dir))

	err := choose(daemon.ChooseMockupRequest{PMKey: "SC-1", Slug: "sc-1-o3-v1", File: "gone.html"})
	assert.Error(t, err)
}

func TestMockupChooser_EmptySlugClears(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1-o3-v1", "sc-1", "02.html")
	reg := testRegistry(t, dir)
	choose := mockupChooserFunc(reg)

	require.NoError(t, choose(daemon.ChooseMockupRequest{PMKey: "SC-1", Slug: "sc-1-o3-v1", File: "02.html"}))
	require.NoError(t, choose(daemon.ChooseMockupRequest{PMKey: "SC-1"}))

	_, ok := mockups.NewStore(mockups.PathIn(dir)).ChosenFor("SC-1")
	assert.False(t, ok)
}

func TestMockupPruner_RefusesRoot(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1", "", "01.html")
	prune := mockupPrunerFunc(testRegistry(t, dir))

	err := prune(daemon.PruneMockupRequest{PMKey: "SC-1", Slug: "sc-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot prune the root mockup group")
	assert.DirExists(t, filepath.Join(dir, "mockups", "sc-1"))
}

func TestMockupPruner_ArchivesSubtreeAndClearsWinnerInside(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1", "", "01.html")                       // root
	writeGroup(t, dir, "sc-1-o1-v1", "sc-1", "01.html")             // A
	writeGroup(t, dir, "sc-1-o1-v1-o1-v1", "sc-1-o1-v1", "01.html") // B (child of A)
	reg := testRegistry(t, dir)

	store := mockups.NewStore(mockups.PathIn(dir))
	require.NoError(t, store.Choose("SC-1", mockups.Choice{Slug: "sc-1-o1-v1-o1-v1", File: "01.html"}))

	prune := mockupPrunerFunc(reg)
	require.NoError(t, prune(daemon.PruneMockupRequest{PMKey: "SC-1", Slug: "sc-1-o1-v1"}))

	assert.NoDirExists(t, filepath.Join(dir, "mockups", "sc-1-o1-v1"))
	assert.NoDirExists(t, filepath.Join(dir, "mockups", "sc-1-o1-v1-o1-v1"))
	assert.DirExists(t, filepath.Join(dir, "mockups", ".archive", "sc-1-o1-v1"))
	assert.DirExists(t, filepath.Join(dir, "mockups", ".archive", "sc-1-o1-v1-o1-v1"))
	assert.DirExists(t, filepath.Join(dir, "mockups", "sc-1"), "root must remain")

	_, ok := store.ChosenFor("SC-1")
	assert.False(t, ok, "winner inside the pruned subtree must be cleared")
}

func TestMockupPruner_KeepsWinnerOutsideSubtree(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1", "", "01.html")
	writeGroup(t, dir, "sc-1-o1-v1", "sc-1", "01.html") // pruned branch A
	writeGroup(t, dir, "sc-1-o2-v1", "sc-1", "01.html") // sibling branch, winner here
	reg := testRegistry(t, dir)

	store := mockups.NewStore(mockups.PathIn(dir))
	require.NoError(t, store.Choose("SC-1", mockups.Choice{Slug: "sc-1-o2-v1", File: "01.html"}))

	prune := mockupPrunerFunc(reg)
	require.NoError(t, prune(daemon.PruneMockupRequest{PMKey: "SC-1", Slug: "sc-1-o1-v1"}))

	c, ok := store.ChosenFor("SC-1")
	assert.True(t, ok, "winner on a sibling branch must survive")
	assert.Equal(t, "sc-1-o2-v1", c.Slug)
}

func TestVariationSubtree_OrphanContributesNoChildren(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1", "", "01.html")
	writeGroup(t, dir, "orphan", "gone-parent", "01.html")

	got := variationSubtree(filepath.Join(dir, "mockups"), "sc-1")
	assert.Equal(t, []string{"sc-1"}, got)
}

// Regression for SC-4991 (AD4): the agent's real manifest has an OBJECT
// options array, not the daemon placeholder's empty string array. Before the
// []json.RawMessage change, json.Unmarshal into childManifest failed on this
// shape, so the group contributed no parent link and variationSubtree
// silently dropped it and its descendants.
func TestVariationSubtree_FindsChildrenOfARealManifest(t *testing.T) {
	dir := t.TempDir()
	writeRealGroup(t, dir, "sc-1", "", "01.html")
	writeRealGroup(t, dir, "sc-1-o1-v1", "sc-1", "01.html")

	got := variationSubtree(filepath.Join(dir, "mockups"), "sc-1")
	assert.ElementsMatch(t, []string{"sc-1", "sc-1-o1-v1"}, got)
}

// swapLaunchMockupAgent stubs the package-level launch var and restores it,
// so creator tests can assert what a launch requested without Docker.
func swapLaunchMockupAgent(t *testing.T, fn func(ctx context.Context, name, prompt, projectDir string) error) {
	t.Helper()
	prev := launchMockupAgent
	launchMockupAgent = fn
	t.Cleanup(func() { launchMockupAgent = prev })
}

// Regression for SC-4991: the mockups creator must launch through
// launchMockupAgent (which shares mockups/ into the run) rather than a bare
// Launch call, and the ticket→set link must still be recorded.
func TestMockupsCreator_SharesProjectMockupsDir(t *testing.T) {
	dir := t.TempDir()
	var gotProjectDir string
	calls := 0
	swapLaunchMockupAgent(t, func(_ context.Context, _, _, projectDir string) error {
		calls++
		gotProjectDir = projectDir
		return nil
	})

	create := mockupsCreatorFunc(testRegistry(t, dir))
	require.NoError(t, create(daemon.CreateMocksRequest{PMKey: "SC-1", PMTitle: "t"}))

	assert.Equal(t, 1, calls)
	assert.Equal(t, dir, gotProjectDir)

	store := mockups.NewStore(mockups.PathIn(dir))
	_, ok := store.All()["SC-1"]
	assert.True(t, ok, "ticket->set link must be recorded")
}

// Regression for SC-4991: the variations creator must launch through
// launchMockupAgent, and the reserved child group must stay hidden ("options":
// []) until the agent overwrites it.
func TestVariationsCreator_SharesProjectMockupsDir(t *testing.T) {
	dir := t.TempDir()
	writeGroup(t, dir, "sc-1", "", "01.html")

	var gotProjectDir string
	calls := 0
	swapLaunchMockupAgent(t, func(_ context.Context, _, _, projectDir string) error {
		calls++
		gotProjectDir = projectDir
		return nil
	})

	create := variationsCreatorFunc(testRegistry(t, dir))
	require.NoError(t, create(daemon.CreateVariationsRequest{
		PMKey: "SC-1", ParentSlug: "sc-1", ParentFile: "01.html",
		Feature: "f", Instructions: "i",
	}))

	assert.Equal(t, 1, calls)
	assert.Equal(t, dir, gotProjectDir)

	data, err := os.ReadFile(filepath.Join(dir, "mockups", "sc-1-o1-v1", "index.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"options":[]`)
}

// Regression for SC-4991: a board-stage launcher (no sharedPaths) must
// request no shared paths, and the mockup launcher must request exactly
// "mockups" — the directory name the desktop reads from the project.
func TestLaunchMockupAgent_RequestsMockupsShare(t *testing.T) {
	mockupOpts := dockerAgentLauncher{sharedPaths: []string{mockupsDirName}}.
		startOpts("mockups-sc-1", "p", "/proj", "/proj", "")
	assert.Equal(t, []string{"mockups"}, mockupOpts.SharedPaths)

	boardOpts := dockerAgentLauncher{}.startOpts("board-sc-1-x", "p", "/proj", "/proj", "")
	assert.Nil(t, boardOpts.SharedPaths)
}
