package cmddaemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/codenav/store"
	"github.com/gethuman-sh/human/internal/daemon"
)

// symbolCount opens the index and returns the symbol count of the first
// project, or 0 if the index is not yet queryable.
func symbolCount(t *testing.T, dbPath string) int {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		return 0
	}
	defer func() { _ = st.Close() }()
	projs, err := st.ListProjects()
	if err != nil || len(projs) == 0 {
		return 0
	}
	return projs[0].Symbols
}

func TestPollInterval_backoffWhenWatcherActive(t *testing.T) {
	assert.Equal(t, codenavPollSafetyNetInterval, pollInterval(true))
	assert.Equal(t, codenavPollInterval, pollInterval(false))
	assert.Equal(t, 30*time.Minute, codenavPollSafetyNetInterval)
	assert.Equal(t, 5*time.Minute, codenavPollInterval)
}

func TestStartCodenavWatcher_refreshesOnFileChange(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Prime the index with a startup pass so we observe the watcher-driven delta.
	indexRegisteredProjects(ctx, reg, dbPath, zerolog.Nop())
	before := symbolCount(t, dbPath)
	require.Positive(t, before)

	w, err := startCodenavWatcher(ctx, reg, dbPath, zerolog.Nop(), withDebounce(30*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	dir := reg.Entries()[0].Dir
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc A() {}\n\nfunc B() {}\n\nfunc main() { A(); B() }\n"), 0o644))

	require.Eventually(t, func() bool {
		return symbolCount(t, dbPath) > before
	}, 3*time.Second, 20*time.Millisecond)
}

func TestStartCodenavWatcher_debouncesRapidEdits(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	w, err := startCodenavWatcher(ctx, reg, dbPath, zerolog.Nop(),
		withDebounce(80*time.Millisecond),
		withRefresh(func(context.Context, string, daemon.ProjectEntry, zerolog.Logger) {
			atomic.AddInt32(&calls, 1)
		}))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	dir := reg.Entries()[0].Dir
	for i := 0; i < 5; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
			[]byte("package main\n\nfunc A() {}\n\nfunc main() { A() }\n"), 0o644))
		time.Sleep(10 * time.Millisecond)
	}

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) >= 1
	}, 2*time.Second, 20*time.Millisecond)
	// After the burst settles, exactly one debounced refresh should have run.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestCodenavWatcher_serializesConcurrentRefresh(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := make(chan struct{})
	var concurrent, maxConcurrent, total int32
	w, err := startCodenavWatcher(ctx, reg, dbPath, zerolog.Nop(),
		withDebounce(10*time.Millisecond),
		withRefresh(func(context.Context, string, daemon.ProjectEntry, zerolog.Logger) {
			cur := atomic.AddInt32(&concurrent, 1)
			for {
				m := atomic.LoadInt32(&maxConcurrent)
				if cur <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, cur) {
					break
				}
			}
			atomic.AddInt32(&total, 1)
			<-release
			atomic.AddInt32(&concurrent, -1)
		}))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	e := reg.Entries()[0]
	// Fire the first refresh, then queue several more while it is blocked.
	w.schedule(ctx, e)
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&concurrent) == 1
	}, time.Second, 5*time.Millisecond)
	for i := 0; i < 4; i++ {
		w.fire(ctx, e) // arrives mid-refresh -> at most one queued follow-up
	}
	close(release)

	// The first refresh plus exactly one coalesced follow-up (the 4 mid-flight
	// events collapse into a single re-armed refresh, not four).
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&total) == 2
	}, 2*time.Second, 10*time.Millisecond)
	assert.LessOrEqual(t, atomic.LoadInt32(&maxConcurrent), int32(1))
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&concurrent) == 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestStartCodenavWatcher_watchesNewDirectory(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	indexRegisteredProjects(ctx, reg, dbPath, zerolog.Nop())
	before := symbolCount(t, dbPath)

	w, err := startCodenavWatcher(ctx, reg, dbPath, zerolog.Nop(), withDebounce(30*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	dir := reg.Entries()[0].Dir
	sub := filepath.Join(dir, "pkgc")
	require.NoError(t, os.Mkdir(sub, 0o755))
	// Give the Create event time to register the new dir before writing into it.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(sub, "c.go"),
		[]byte("package pkgc\n\nfunc C() {}\n"), 0o644))

	require.Eventually(t, func() bool {
		return symbolCount(t, dbPath) > before
	}, 3*time.Second, 20*time.Millisecond)
}

func TestStartCodenavWatcher_bulkChangePickedUp(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	indexRegisteredProjects(ctx, reg, dbPath, zerolog.Nop())
	before := symbolCount(t, dbPath)

	w, err := startCodenavWatcher(ctx, reg, dbPath, zerolog.Nop(), withDebounce(50*time.Millisecond))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	dir := reg.Entries()[0].Dir
	for i := 0; i < 8; i++ {
		name := filepath.Join(dir, "bulk"+string(rune('a'+i))+".go")
		body := "package main\n\nfunc Bulk" + string(rune('A'+i)) + "() {}\n"
		require.NoError(t, os.WriteFile(name, []byte(body), 0o644))
	}

	require.Eventually(t, func() bool {
		return symbolCount(t, dbPath) >= before+8
	}, 3*time.Second, 20*time.Millisecond)
}

func TestStartCodenavWatcher_setupFailureReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	reg, err := daemon.NewProjectRegistry([]string{missing})
	require.NoError(t, err)
	dbPath := filepath.Join(t.TempDir(), "codenav.db")

	w, err := startCodenavWatcher(context.Background(), reg, dbPath, zerolog.Nop())
	require.Error(t, err)
	assert.Nil(t, w)
}

func TestRunCodenavIndexLoop_degradesToPollOnWatcherFailure(t *testing.T) {
	// A registry pointing at a non-existent dir makes the watcher fail to start;
	// the loop must still run its startup pass and shut down cleanly.
	missing := filepath.Join(t.TempDir(), "gone")
	reg, err := daemon.NewProjectRegistry([]string{missing})
	require.NoError(t, err)
	dbPath := filepath.Join(t.TempDir(), "codenav.db")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runCodenavIndexLoop(ctx, reg, dbPath, zerolog.Nop())
		close(done)
	}()
	// No panic; cancel and join cleanly.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not stop after cancel")
	}
}

func TestCodenavWatcher_runRecoversFromPanic(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	w, err := startCodenavWatcher(ctx, reg, dbPath, zerolog.Nop(),
		withDebounce(10*time.Millisecond),
		withRefresh(func(context.Context, string, daemon.ProjectEntry, zerolog.Logger) {
			atomic.AddInt32(&calls, 1)
			panic("boom")
		}))
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	e := reg.Entries()[0]
	// A panicking refresh must be recovered — no crash — and must not leave the
	// project wedged as permanently in-flight (a follow-up refresh still runs).
	assert.NotPanics(t, func() { w.fire(ctx, e) })
	assert.NotPanics(t, func() { w.fire(ctx, e) })
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

	w.mu.Lock()
	stuck := w.inFlight[e.Name]
	w.mu.Unlock()
	assert.False(t, stuck, "in-flight guard must be cleared after a panicking refresh")
}

func TestRefreshProject_skippedNoLog(t *testing.T) {
	reg, dbPath := writeGoProject(t)
	ctx := context.Background()
	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	e := reg.Entries()[0]
	refreshProject(ctx, st, e, zerolog.Nop()) // first pass indexes
	// Second pass on an unchanged project must be a no-op skip (no error/panic).
	assert.NotPanics(t, func() { refreshProject(ctx, st, e, zerolog.Nop()) })
}
