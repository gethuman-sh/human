package cmddaemon

import (
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
		Options: []string{},
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.json"), data, 0o600))
	if file != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("<html></html>"), 0o600))
	}
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
	writeGroup(t, dir, "sc-1", "", "01.html")             // root
	writeGroup(t, dir, "sc-1-o1-v1", "sc-1", "01.html")   // A
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
	writeGroup(t, dir, "sc-1-o1-v1", "sc-1", "01.html")   // pruned branch A
	writeGroup(t, dir, "sc-1-o2-v1", "sc-1", "01.html")   // sibling branch, winner here
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
