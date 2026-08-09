package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, yaml string) *Document {
	t.Helper()
	doc, err := Parse([]byte(yaml), "test.yaml")
	require.NoError(t, err)
	return doc
}

func render(t *testing.T, doc *Document) string {
	t.Helper()
	data, err := doc.Bytes()
	require.NoError(t, err)
	return string(data)
}

func TestTrackers_readsEverySection(t *testing.T) {
	doc := parse(t, `
shortcuts:
  - name: board
    role: pm
    token: sc
githubs:
  - name: work
    token: gh
    projects:
      - acme/web
`)

	got := doc.Trackers()
	require.Len(t, got, 2)
	// Sections in alphabetical order, so two reports of one config compare.
	assert.Equal(t, "github", got[0].Kind)
	assert.Equal(t, []string{"acme/web"}, got[0].Projects)
	assert.Equal(t, "shortcut", got[1].Kind)
	assert.Equal(t, "pm", got[1].Role)
}

func TestForges_defaultsKind(t *testing.T) {
	doc := parse(t, "forges:\n  - name: prs\n    token: t\n")

	got := doc.Forges()
	require.Len(t, got, 1)
	assert.Equal(t, "github", got[0].Kind, "github is the only forge, so it is what an unstated kind means")
}

func TestTrackersAndForges_emptyDocument(t *testing.T) {
	doc := parse(t, "")
	assert.Empty(t, doc.Trackers())
	assert.Empty(t, doc.Forges())
}

func TestAddTracker_writesTheKindsSection(t *testing.T) {
	doc := parse(t, "")
	require.NoError(t, doc.AddTracker(Tracker{Kind: "shortcut", Name: "board", Role: "pm", Token: "sc"}))

	out := render(t, doc)
	assert.Contains(t, out, "shortcuts:")
	assert.Contains(t, out, "name: board")
	assert.Contains(t, out, "role: pm")

	// And it reads back as what was asked for, which is the round trip that
	// makes the object worth having.
	reread := parse(t, out)
	require.Len(t, reread.Trackers(), 1)
	assert.Equal(t, "shortcut", reread.Trackers()[0].Kind)
}

// An empty field writes nothing: a new entry should carry what was asked for,
// not a row of blank keys nobody typed.
func TestAddTracker_omitsWhatWasNotGiven(t *testing.T) {
	doc := parse(t, "")
	require.NoError(t, doc.AddTracker(Tracker{Kind: "linear", Name: "work", Token: "t"}))

	out := render(t, doc)
	assert.NotContains(t, out, "role:")
	assert.NotContains(t, out, "url:")
	assert.NotContains(t, out, "projects:")
}

func TestAddTracker_rejectsUnknownKind(t *testing.T) {
	doc := parse(t, "")
	require.Error(t, doc.AddTracker(Tracker{Kind: "bugzilla", Name: "x"}))
}

func TestAddTracker_rejectsANameless(t *testing.T) {
	doc := parse(t, "")
	require.Error(t, doc.AddTracker(Tracker{Kind: "jira"}))
}

// Adding over an existing name is an error, not a silent overwrite: two entries
// of one kind sharing a name make every by-name resolution ambiguous, and
// replacing someone's entry is not what "add" means.
func TestAddTracker_refusesADuplicateName(t *testing.T) {
	doc := parse(t, "jiras:\n  - name: work\n    token: t\n")
	err := doc.AddTracker(Tracker{Kind: "jira", Name: "work", Token: "other"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already configured")
}

func TestAddForge_andDuplicate(t *testing.T) {
	doc := parse(t, "")
	require.NoError(t, doc.AddForge(Forge{Name: "prs", Token: "t"}))
	require.Error(t, doc.AddForge(Forge{Name: "prs", Token: "t"}))

	out := render(t, doc)
	assert.Contains(t, out, "forges:")
	assert.NotContains(t, out, "kind:", "github is the default, so it is not written out")
}

func TestRemoveTracker_dropsTheSectionWhenEmptied(t *testing.T) {
	doc := parse(t, "jiras:\n  - name: work\n    token: t\n")

	assert.True(t, doc.RemoveTracker("jira", "work"))
	assert.False(t, doc.RemoveTracker("jira", "work"), "a second removal has nothing to remove")

	out := render(t, doc)
	assert.NotContains(t, out, "jiras", "an empty section reads as configured-but-broken")
}

// The migration's whole job, as a method: the node moves, so the author's
// comment moves with it, and tracker-only fields are left behind.
func TestMoveTrackerToForge_carriesCommentsNotTrackerFields(t *testing.T) {
	doc := parse(t, `githubs:
  # the token lives in 1Password
  - name: human
    token: gh://token
    projects:
      - acme/web
    create_in: acme/web
`)

	moved, err := doc.MoveTrackerToForge("github", "human")
	require.NoError(t, err)
	assert.True(t, moved)

	out := render(t, doc)
	assert.Contains(t, out, "forges:")
	assert.Contains(t, out, "# the token lives in 1Password")
	assert.Contains(t, out, "gh://token")
	assert.NotContains(t, out, "projects")
	assert.NotContains(t, out, "create_in")
	assert.NotContains(t, sectionKeys(out), "githubs")
}

// The leftover case: the credentials are already in forges:, so the tracker
// entry is a remnant and removing it deletes nothing ([SC-3887]).
func TestMoveTrackerToForge_clearsALeftoverWithoutDuplicating(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: human\n    token: t\nforges:\n  - name: human\n    token: t\n")

	moved, err := doc.MoveTrackerToForge("github", "human")
	require.NoError(t, err)
	assert.True(t, moved)

	assert.Empty(t, doc.Trackers())
	assert.Len(t, doc.Forges(), 1, "the forge that was already there is not duplicated")
}

func TestMoveTrackerToForge_absentEntry(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: other\n    token: t\n")
	moved, err := doc.MoveTrackerToForge("github", "human")
	require.NoError(t, err)
	assert.False(t, moved)
}

// Everything the file carries that this binary has no opinion about survives a
// write. A tool that eats your comments when you ask it to change one line has
// damaged the file it was asked to edit.
func TestWrite_preservesCommentsAndUnknownSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".humanconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`# my project
project: cli

something_new:
  keep: me

shortcuts:
  - name: board   # the PM tracker
    token: sc
`), 0o600))

	doc, err := Load(dir)
	require.NoError(t, err)
	require.NoError(t, doc.AddForge(Forge{Name: "prs", Token: "t"}))
	require.NoError(t, doc.Write())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	assert.Contains(t, out, "# my project")
	assert.Contains(t, out, "something_new")
	assert.Contains(t, out, "keep: me")
	assert.Contains(t, out, "# the PM tracker")
	assert.Contains(t, out, "forges:")
}

func TestWrite_keepsFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".humanconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("project: cli\n"), 0o600))

	doc, err := Load(dir)
	require.NoError(t, err)
	require.NoError(t, doc.Write())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a config holding secrets must not be widened by an edit")
}

// A missing file is not an error: adding the first tracker to a fresh project
// takes the same path as editing an existing one.
func TestLoad_missingFileYieldsAWritableDocument(t *testing.T) {
	dir := t.TempDir()
	doc, err := Load(dir)
	require.NoError(t, err)
	require.NoError(t, doc.AddTracker(Tracker{Kind: "shortcut", Name: "board", Token: "t"}))
	require.NoError(t, doc.Write())

	assert.FileExists(t, filepath.Join(dir, ".humanconfig.yaml"))
}

func TestLoad_reportsABrokenFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("just a string"), 0o600))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a mapping")
}

// sectionKeys is the top-level keys of a config, so a test can assert a section
// is gone without matching the word inside a comment that names it.
func sectionKeys(config string) []string {
	var keys []string
	for _, line := range strings.Split(config, "\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, ok := strings.Cut(line, ":"); ok {
			keys = append(keys, key)
		}
	}
	return keys
}
