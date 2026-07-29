package boardcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "boardcache.json"))
}

func TestStore_SaveLoad_roundTrip(t *testing.T) {
	s := newTestStore(t)
	snap := json.RawMessage([]byte(`{"cards":[{"key":"1"}]}`))

	require.NoError(t, s.Save("proj", snap))

	got, ok := s.Load("proj")
	assert.True(t, ok)
	assert.JSONEq(t, string(snap), string(got))
}

func TestStore_Load_projectMismatch(t *testing.T) {
	// A snapshot captured for one project must never paint another project's
	// board, so a mismatched key reads as a clean miss.
	s := newTestStore(t)
	require.NoError(t, s.Save("projA", json.RawMessage([]byte(`{"cards":[]}`))))

	got, ok := s.Load("projB")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestStore_Load_missingFile(t *testing.T) {
	s := newTestStore(t)

	got, ok := s.Load("proj")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestStore_Load_corruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path, []byte("not json"), 0o600))

	got, ok := s.Load("proj")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestStore_Load_wrongVersion(t *testing.T) {
	// A future format must read as empty rather than be misinterpreted.
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path,
		[]byte(`{"version":999,"project":"p","snapshot":{}}`), 0o600))

	got, ok := s.Load("p")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestStore_Load_emptySnapshot(t *testing.T) {
	// A record with no snapshot is nothing to paint — treat it as a miss.
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path,
		[]byte(`{"version":1,"project":"p"}`), 0o600))

	got, ok := s.Load("p")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestStore_Save_accumulatesPerProject(t *testing.T) {
	// Regression (SC-1654): saving a second project must NOT evict the first —
	// switching back must still hit the cache instead of a cold spinner.
	s := newTestStore(t)
	snapA := json.RawMessage([]byte(`{"cards":[{"key":"a"}]}`))
	snapB := json.RawMessage([]byte(`{"cards":[{"key":"b"}]}`))

	require.NoError(t, s.Save("projA", snapA))
	require.NoError(t, s.Save("projB", snapB))

	gotA, ok := s.Load("projA")
	assert.True(t, ok)
	assert.JSONEq(t, string(snapA), string(gotA))

	gotB, ok := s.Load("projB")
	assert.True(t, ok)
	assert.JSONEq(t, string(snapB), string(gotB))
}

func TestStore_Migration_v1SingleSlotFoldsIntoProject(t *testing.T) {
	// A legacy v1 file (single project+snapshot) is read as that project's slot.
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path,
		[]byte(`{"version":1,"project":"cli","snapshot":{"cards":[{"key":"x"}]}}`), 0o600))

	got, ok := s.Load("cli")
	assert.True(t, ok)
	assert.JSONEq(t, `{"cards":[{"key":"x"}]}`, string(got))

	_, ok = s.Load("roomer")
	assert.False(t, ok, "legacy snapshot must not leak into another project")
}

func TestStore_Save_atomicNoTmpLeft(t *testing.T) {
	// A successful save leaves the snapshot at the final path and no dangling
	// temp file from the atomic write.
	s := newTestStore(t)
	require.NoError(t, s.Save("proj", json.RawMessage([]byte(`{"cards":[]}`))))

	_, err := os.Stat(s.path)
	assert.NoError(t, err)

	_, err = os.Stat(s.path + ".tmp")
	assert.True(t, os.IsNotExist(err))
}

func TestStore_DefaultPath(t *testing.T) {
	assert.True(t, strings.HasSuffix(DefaultPath(),
		filepath.Join(".human", "boardcache.json")))
}
