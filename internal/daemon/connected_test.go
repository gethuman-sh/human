package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectedTracker_TouchAndPIDs(t *testing.T) {
	tr := NewConnectedTracker()
	assert.Empty(t, tr.PIDs())

	tr.Touch(100)
	tr.Touch(200)
	tr.Touch(100) // refresh
	assert.Equal(t, []int{100, 200}, tr.PIDs())
}

func TestConnectedTracker_Prune(t *testing.T) {
	tr := NewConnectedTracker()
	tr.Touch(100)
	tr.Touch(200)

	// Manually set PID 100 to be old.
	tr.mu.Lock()
	tr.pids[100] = time.Now().Add(-60 * time.Second)
	tr.mu.Unlock()

	tr.Prune(30 * time.Second)
	assert.Equal(t, []int{200}, tr.PIDs())
}

func TestConnectedTracker_PruneAll(t *testing.T) {
	tr := NewConnectedTracker()
	tr.Touch(100)

	tr.mu.Lock()
	tr.pids[100] = time.Now().Add(-60 * time.Second)
	tr.mu.Unlock()

	tr.Prune(30 * time.Second)
	assert.Empty(t, tr.PIDs())
}

func TestWriteReadConnected_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connected.json")

	err := WriteConnected(path, []int{100, 200, 300})
	require.NoError(t, err)

	got := ReadConnected(path)
	assert.Equal(t, []int{100, 200, 300}, got)
}

func TestReadConnected_MissingFile(t *testing.T) {
	got := ReadConnected(filepath.Join(t.TempDir(), "nonexistent.json"))
	assert.Nil(t, got)
}

func TestReadConnected_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	got := ReadConnected(path)
	assert.Nil(t, got)
}

func TestRemoveConnected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connected.json")
	require.NoError(t, os.WriteFile(path, []byte("[]"), 0o600))

	RemoveConnected(path)
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestWriteConnected_EmptySlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connected.json")

	err := WriteConnected(path, []int{})
	require.NoError(t, err)

	got := ReadConnected(path)
	assert.Equal(t, []int{}, got)
}

// Drive concurrent Touch/Prune/PIDs against the race detector. Touch
// and Prune mutate the internal map and PIDs walks it; without the
// mutex on every path, this test trips a data-race failure.
func TestConnectedTracker_concurrentAccess(t *testing.T) {
	tr := NewConnectedTracker()
	const workers = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers * 3)

	for w := range workers {
		base := w * 1000
		go func() {
			defer wg.Done()
			for i := range iterations {
				tr.Touch(base + i)
			}
		}()
	}
	for range workers {
		go func() {
			defer wg.Done()
			for range iterations {
				_ = tr.PIDs()
			}
		}()
	}
	for range workers {
		go func() {
			defer wg.Done()
			for range iterations {
				tr.Prune(1 * time.Hour)
			}
		}()
	}
	wg.Wait()
}
