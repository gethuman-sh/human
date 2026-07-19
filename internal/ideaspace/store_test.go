package ideaspace

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "ideaspace.json"))
}

func TestStore_SetAndAssignments_RoundTrip(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-123", 2))
	require.NoError(t, s.Set("SC-456", 4))

	assert.Equal(t, map[string]int{"SC-123": 2, "SC-456": 4}, s.Assignments())
}

func TestStore_Set_OverwritesExisting(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-123", 1))
	require.NoError(t, s.Set("SC-123", 3))

	assert.Equal(t, map[string]int{"SC-123": 3}, s.Assignments())
}

func TestStore_Assignments_MissingFile(t *testing.T) {
	s := newTestStore(t)
	assert.Empty(t, s.Assignments())
}

func TestStore_Set_ClampsColumn(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("low", -1))
	require.NoError(t, s.Set("high", 99))

	assert.Equal(t, map[string]int{"low": 0, "high": Columns - 1}, s.Assignments())
}

func TestStore_Assignments_ClampsStoredColumns(t *testing.T) {
	// A hand-edited file may carry out-of-range columns; reads must still
	// place every idea inside the space.
	s := newTestStore(t)
	data := `{"version":1,"ideas":{"low":-3,"high":42,"ok":2}}`
	require.NoError(t, os.WriteFile(s.path, []byte(data), 0o600))

	assert.Equal(t, map[string]int{"low": 0, "high": Columns - 1, "ok": 2}, s.Assignments())
}

func TestStore_Assignments_CorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path, []byte("not json"), 0o600))

	assert.Empty(t, s.Assignments())
}

func TestStore_Set_RecoversFromCorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path, []byte("not json"), 0o600))

	require.NoError(t, s.Set("SC-1", 1))

	assert.Equal(t, map[string]int{"SC-1": 1}, s.Assignments())
}

func TestStore_Assignments_UnknownVersion(t *testing.T) {
	s := newTestStore(t)
	data := `{"version":2,"ideas":{"SC-1":3}}`
	require.NoError(t, os.WriteFile(s.path, []byte(data), 0o600))

	assert.Empty(t, s.Assignments())
}

func TestStore_PruneExcept_RemovesStaleKeys(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Set("keep", 2))
	require.NoError(t, s.Set("stale", 3))

	require.NoError(t, s.PruneExcept(map[string]struct{}{"keep": {}}))

	assert.Equal(t, map[string]int{"keep": 2}, s.Assignments())
}

func TestStore_PruneExcept_MissingFileIsNoOp(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.PruneExcept(map[string]struct{}{"any": {}}))

	_, err := os.Stat(s.path)
	assert.True(t, os.IsNotExist(err))
}

func TestStore_PruneExcept_NothingStaleLeavesFileUntouched(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Set("keep", 1))
	before, err := os.Stat(s.path)
	require.NoError(t, err)

	require.NoError(t, s.PruneExcept(map[string]struct{}{"keep": {}, "extra": {}}))

	after, err := os.Stat(s.path)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
	assert.Equal(t, map[string]int{"keep": 1}, s.Assignments())
}

func TestStore_Set_CreatesParentDir(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nested", ".human", "ideaspace.json"))

	require.NoError(t, s.Set("SC-1", 0))

	assert.Equal(t, map[string]int{"SC-1": 0}, s.Assignments())
}

func TestDefaultPath_EndsWithConventionalLocation(t *testing.T) {
	path := DefaultPath()
	assert.Equal(t, "ideaspace.json", filepath.Base(path))
	assert.Equal(t, ".human", filepath.Base(filepath.Dir(path)))
}

// Drive concurrent Set/Assignments/PruneExcept against the race detector; the
// mutex must make every read-modify-write cycle atomic.
func TestStore_ConcurrentAccess(t *testing.T) {
	s := newTestStore(t)
	const workers = 8
	const iterations = 25

	var wg sync.WaitGroup
	wg.Add(workers * 3)
	for w := range workers {
		key := string(rune('a' + w))
		go func() {
			defer wg.Done()
			for i := range iterations {
				assert.NoError(t, s.Set(key, i%Columns))
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				_ = s.Assignments()
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				assert.NoError(t, s.PruneExcept(map[string]struct{}{
					"a": {}, "b": {}, "c": {}, "d": {}, "e": {}, "f": {}, "g": {}, "h": {},
				}))
			}
		}()
	}
	wg.Wait()

	// Every surviving assignment must be in range.
	for _, col := range s.Assignments() {
		assert.GreaterOrEqual(t, col, 0)
		assert.Less(t, col, Columns)
	}
}
