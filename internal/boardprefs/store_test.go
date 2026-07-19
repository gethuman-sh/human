package boardprefs

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
	return NewStore(filepath.Join(t.TempDir(), "boardprefs.json"))
}

func TestStore_SetOrder_RoundTrip(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.SetOrder("product", []string{"SC-2", "SC-1"}))
	require.NoError(t, s.SetOrder("building", []string{"SC-9"}))

	p := s.Snapshot()
	assert.Equal(t, []string{"SC-2", "SC-1"}, p.Columns["product"])
	assert.Equal(t, []string{"SC-9"}, p.Columns["building"])
}

func TestStore_SetOrder_ReplacesQueue(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.SetOrder("product", []string{"SC-1", "SC-2"}))
	require.NoError(t, s.SetOrder("product", []string{"SC-2", "SC-1"}))

	assert.Equal(t, []string{"SC-2", "SC-1"}, s.Snapshot().Columns["product"])
}

func TestStore_SetHidden_AddAndRemove(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.SetHidden("SC-1", true))
	require.NoError(t, s.SetHidden("SC-2", true))
	_, hidden := s.Snapshot().Hidden["SC-1"]
	assert.True(t, hidden)

	require.NoError(t, s.SetHidden("SC-1", false))
	p := s.Snapshot()
	_, hidden = p.Hidden["SC-1"]
	assert.False(t, hidden)
	_, hidden = p.Hidden["SC-2"]
	assert.True(t, hidden)
}

func TestStore_SetHidden_Idempotent(t *testing.T) {
	// Hiding twice must not duplicate the key on disk (an unhide would then
	// only remove one copy).
	s := newTestStore(t)

	require.NoError(t, s.SetHidden("SC-1", true))
	require.NoError(t, s.SetHidden("SC-1", true))
	require.NoError(t, s.SetHidden("SC-1", false))

	_, hidden := s.Snapshot().Hidden["SC-1"]
	assert.False(t, hidden)
}

func TestStore_Snapshot_MissingFile(t *testing.T) {
	s := newTestStore(t)
	p := s.Snapshot()
	assert.Empty(t, p.Columns)
	assert.Empty(t, p.Hidden)
}

func TestStore_Snapshot_CorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path, []byte("{not json"), 0o600))

	p := s.Snapshot()
	assert.Empty(t, p.Columns)
	assert.Empty(t, p.Hidden)
}

func TestStore_Snapshot_FutureVersion(t *testing.T) {
	// A future format must read as empty rather than be misinterpreted.
	s := newTestStore(t)
	data := `{"version":2,"columns":{"product":["SC-1"]},"hidden":["SC-1"]}`
	require.NoError(t, os.WriteFile(s.path, []byte(data), 0o600))

	p := s.Snapshot()
	assert.Empty(t, p.Columns)
	assert.Empty(t, p.Hidden)
}

func TestStore_PruneExcept_DropsVanishedTickets(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.SetOrder("product", []string{"SC-1", "SC-2", "SC-3"}))
	require.NoError(t, s.SetHidden("SC-2", true))
	require.NoError(t, s.SetHidden("SC-9", true))

	require.NoError(t, s.PruneExcept(map[string]struct{}{
		"SC-1": {}, "SC-2": {},
	}))

	p := s.Snapshot()
	assert.Equal(t, []string{"SC-1", "SC-2"}, p.Columns["product"])
	_, hidden := p.Hidden["SC-2"]
	assert.True(t, hidden)
	_, gone := p.Hidden["SC-9"]
	assert.False(t, gone)
}

func TestStore_PruneExcept_MissingFileIsNoOp(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.PruneExcept(map[string]struct{}{"SC-1": {}}))
	_, err := os.Stat(s.path)
	assert.True(t, os.IsNotExist(err))
}

func TestStore_PruneExcept_NoChangeNoWrite(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.SetOrder("product", []string{"SC-1"}))
	before, err := os.ReadFile(s.path)
	require.NoError(t, err)

	require.NoError(t, s.PruneExcept(map[string]struct{}{"SC-1": {}}))

	after, err := os.ReadFile(s.path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestStore_ConcurrentWrites(t *testing.T) {
	// Racing binding calls must not lose updates; the mutex serializes the
	// read-modify-write cycles.
	s := newTestStore(t)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.SetOrder("product", []string{"SC-1"})
		}()
		go func() {
			defer wg.Done()
			_ = s.SetHidden("SC-2", true)
		}()
	}
	wg.Wait()

	p := s.Snapshot()
	assert.Equal(t, []string{"SC-1"}, p.Columns["product"])
	_, hidden := p.Hidden["SC-2"]
	assert.True(t, hidden)
}
