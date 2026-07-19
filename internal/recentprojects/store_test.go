package recentprojects

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "recentprojects.json"))
}

// validProjectDir creates a real subdirectory under t.TempDir() carrying a
// .humanconfig.yaml, so config.HasConfigFile (and therefore List's prune
// logic) treats it as a live project.
func validProjectDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte("project: "+name+"\n"), 0o644))
	return dir
}

func TestStore_TouchAndList_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	dirA := validProjectDir(t, "a")
	dirB := validProjectDir(t, "b")

	require.NoError(t, s.Touch(dirA, "a"))
	require.NoError(t, s.Touch(dirB, "b"))

	entries := s.List()
	require.Len(t, entries, 2)
	assert.Equal(t, "b", entries[0].Name)
	assert.Equal(t, "a", entries[1].Name)
}

func TestStore_Touch_MovesExistingToFront(t *testing.T) {
	s := newTestStore(t)
	dirA := validProjectDir(t, "a")
	dirB := validProjectDir(t, "b")

	require.NoError(t, s.Touch(dirA, "a"))
	require.NoError(t, s.Touch(dirB, "b"))
	require.NoError(t, s.Touch(dirA, "a"))

	entries := s.List()
	require.Len(t, entries, 2)
	assert.Equal(t, "a", entries[0].Name)
	assert.Equal(t, "b", entries[1].Name)
}

func TestStore_Touch_CapsAtMaxEntries(t *testing.T) {
	s := newTestStore(t)

	for i := 0; i < MaxEntries+1; i++ {
		dir := validProjectDir(t, string(rune('a'+i)))
		require.NoError(t, s.Touch(dir, string(rune('a'+i))))
	}

	entries := s.List()
	assert.Len(t, entries, MaxEntries)
	// The oldest touch (the first one, "a") must have been dropped.
	for _, e := range entries {
		assert.NotEqual(t, "a", e.Name)
	}
}

func TestStore_List_PrunesMissingConfig(t *testing.T) {
	s := newTestStore(t)
	dirA := validProjectDir(t, "a")
	require.NoError(t, s.Touch(dirA, "a"))

	require.NoError(t, os.Remove(filepath.Join(dirA, ".humanconfig.yaml")))

	assert.Empty(t, s.List())

	// The persisted file must no longer contain dirA either.
	data, err := os.ReadFile(s.path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), dirA)
}

func TestStore_List_MissingFile(t *testing.T) {
	s := newTestStore(t)

	assert.Empty(t, s.List())
}

func TestDefaultPath_EndsWithConventionalLocation(t *testing.T) {
	path := DefaultPath()
	assert.Equal(t, "recentprojects.json", filepath.Base(path))
	assert.Equal(t, ".human", filepath.Base(filepath.Dir(path)))
}
