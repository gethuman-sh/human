package cmddaemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/codenav/store"
	"github.com/gethuman-sh/human/internal/daemon"
)

// writeGoProject lays down a minimal Go module so a real indexer backend
// produces symbols, and returns a registry pointing at it.
func writeGoProject(t *testing.T) (*daemon.ProjectRegistry, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/p\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc A() {}\n\nfunc main() { A() }\n"), 0o644))
	reg, err := daemon.NewProjectRegistry([]string{dir})
	require.NoError(t, err)
	return reg, filepath.Join(t.TempDir(), "codenav.db")
}

func TestIndexRegisteredProjects_indexesEach(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	indexRegisteredProjects(context.Background(), reg, dbPath, zerolog.Nop())

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()
	projs, err := st.ListProjects()
	require.NoError(t, err)
	require.Len(t, projs, 1)
	assert.Equal(t, reg.Entries()[0].Name, projs[0].Name)
	assert.Positive(t, projs[0].Symbols)
}

func TestRunCodenavIndexLoop_indexesThenStopsOnCancel(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runCodenavIndexLoop(ctx, reg, dbPath, zerolog.Nop())
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })

	// The loop indexes once at startup before the first ticker tick; poll until
	// that startup pass lands the project, then let cleanup cancel and join.
	require.Eventually(t, func() bool {
		st, err := store.Open(dbPath)
		if err != nil {
			return false
		}
		defer func() { _ = st.Close() }()
		projs, err := st.ListProjects()
		return err == nil && len(projs) == 1
	}, 5*time.Second, 20*time.Millisecond)
}

func TestCheckCodenavIndex_allIndexed(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	indexRegisteredProjects(context.Background(), reg, dbPath, zerolog.Nop())

	ok, detail := checkCodenavIndex(reg, dbPath)
	assert.True(t, ok)
	assert.Contains(t, detail, "1 project(s) indexed")
}

func TestCheckCodenavIndex_missing(t *testing.T) {
	reg, dbPath := writeGoProject(t) // registry populated, but nothing indexed yet
	ok, detail := checkCodenavIndex(reg, dbPath)
	assert.True(t, ok) // a warming index degrades gracefully, never blocks
	assert.Contains(t, detail, "not yet indexed")
}

func TestCheckCodenavIndex_unreadable(t *testing.T) {
	reg, _ := writeGoProject(t)
	// Pointing the db path at an existing directory makes SQLite fail to open
	// it as a database file — a genuine fault, not a warming index.
	ok, detail := checkCodenavIndex(reg, t.TempDir())
	assert.False(t, ok)
	assert.Contains(t, detail, "cannot open")
}
