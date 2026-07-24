package cmddaemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/codenav/index"
	"github.com/gethuman-sh/human/internal/codenav/store"
	"github.com/gethuman-sh/human/internal/daemon"
)

// codenavDebounceWindow is how long the watcher waits after the last file event
// for a project before refreshing its index, so a burst of edits (or a branch
// switch touching many files) coalesces into a single refresh.
const codenavDebounceWindow = time.Second

// codenavWatcher watches every registered project's tree and refreshes an
// affected project's index after a debounce window. It never affects the poll:
// if it cannot start, runCodenavIndexLoop keeps the poll at the primary cadence.
type codenavWatcher struct {
	reg      *daemon.ProjectRegistry
	dbPath   string
	logger   zerolog.Logger
	fsw      *fsnotify.Watcher
	debounce time.Duration

	// refresh is the per-project refresh action; injectable for tests.
	refresh func(ctx context.Context, dbPath string, e daemon.ProjectEntry, logger zerolog.Logger)

	mu       sync.Mutex
	timers   map[string]*time.Timer         // project name -> pending debounce timer
	inFlight map[string]bool                // project name -> a refresh is currently running
	pending  map[string]daemon.ProjectEntry // project name -> one queued follow-up (events arrived mid-refresh)
}

// codenavWatcherOption configures a codenavWatcher before its dispatch goroutine
// starts. Applying config pre-start keeps it off the goroutine's read path, so no
// synchronization is needed for the debounce window or the injected refresh.
type codenavWatcherOption func(*codenavWatcher)

// withDebounce overrides the debounce window (tests keep it short to stay fast).
func withDebounce(d time.Duration) codenavWatcherOption {
	return func(w *codenavWatcher) { w.debounce = d }
}

// withRefresh injects the per-project refresh action (tests count/serialize it).
func withRefresh(f func(ctx context.Context, dbPath string, e daemon.ProjectEntry, logger zerolog.Logger)) codenavWatcherOption {
	return func(w *codenavWatcher) { w.refresh = f }
}

// startCodenavWatcher creates the watcher, adds a recursive set of directory
// watches for every registered project, and starts the dispatch goroutine.
// It returns the watcher (an io.Closer) to stop watching. On any setup failure it
// returns a nil watcher and an error, and the caller degrades to poll-only.
func startCodenavWatcher(ctx context.Context, reg *daemon.ProjectRegistry, dbPath string, logger zerolog.Logger, opts ...codenavWatcherOption) (*codenavWatcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating codenav file watcher")
	}
	w := &codenavWatcher{
		reg:      reg,
		dbPath:   dbPath,
		logger:   logger,
		fsw:      fsw,
		debounce: codenavDebounceWindow,
		refresh:  refreshOneProject,
		timers:   map[string]*time.Timer{},
		inFlight: map[string]bool{},
		pending:  map[string]daemon.ProjectEntry{},
	}
	for _, o := range opts {
		o(w)
	}
	added := 0
	for _, e := range reg.Entries() {
		n, err := w.addTree(e.Dir)
		if err != nil {
			_ = fsw.Close()
			return nil, errors.WrapWithDetails(err, "watching project tree", "project", e.Name, "dir", e.Dir)
		}
		added += n
	}
	logger.Info().Int("dirs", added).Int("projects", len(reg.Entries())).
		Msg("codenav: file watcher active, poll demoted to safety net")
	go w.run(ctx)
	return w, nil
}

// addTree adds a non-recursive fsnotify watch to root and every descendant
// directory the indexer would descend into (mirrors index.SkipDir), returning
// the count of directories watched.
func (w *codenavWatcher) addTree(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err // the project root itself is unwalkable: a real setup failure
			}
			return nil // unreadable descendant: skip, do not abort the whole walk
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && index.SkipDir(d.Name()) {
			return filepath.SkipDir
		}
		if err := w.fsw.Add(path); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

// run is the dispatch loop. A panic here is recovered and logged so a watcher
// fault never crashes the daemon; the poll safety net keeps the index fresh.
func (w *codenavWatcher) run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error().Interface("panic", r).Msg("codenav: file watcher panicked, poll safety net remains active")
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleEvent(ctx, ev)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// ErrEventOverflow (inotify queue full, bulk changes) lands here; the
			// poll safety net recovers whatever events were dropped.
			w.logger.Warn().Err(err).Msg("codenav: file watcher error")
		}
	}
}

// handleEvent watches newly created directories and schedules a debounced
// refresh for the project owning the event path.
func (w *codenavWatcher) handleEvent(ctx context.Context, ev fsnotify.Event) {
	if ev.Op&fsnotify.Create != 0 {
		if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() && !index.SkipDir(filepath.Base(ev.Name)) {
			if _, aerr := w.addTree(ev.Name); aerr != nil {
				w.logger.Warn().Err(aerr).Str("dir", ev.Name).Msg("codenav: watching new directory failed")
			}
		}
	}
	e, ok := w.reg.Resolve(filepath.Dir(ev.Name))
	if !ok {
		return
	}
	w.schedule(ctx, e)
}

// schedule (re)arms the debounce timer for a project; each event within the
// window pushes the refresh out, coalescing a burst into one refresh.
func (w *codenavWatcher) schedule(ctx context.Context, e daemon.ProjectEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.timers[e.Name]; ok {
		t.Reset(w.debounce)
		return
	}
	w.timers[e.Name] = time.AfterFunc(w.debounce, func() { w.fire(ctx, e) })
}

// fire runs a project's refresh, serialized per project. If a refresh for this
// project is already in flight (it outran the debounce window), it records one
// queued follow-up instead of starting a second concurrent refresh, then re-arms
// the debounce when the in-flight refresh completes. This guarantees at most one
// refresh per project at a time while never dropping edits that land mid-refresh.
func (w *codenavWatcher) fire(ctx context.Context, e daemon.ProjectEntry) {
	w.mu.Lock()
	delete(w.timers, e.Name)
	if w.inFlight[e.Name] {
		w.pending[e.Name] = e
		w.mu.Unlock()
		return
	}
	w.inFlight[e.Name] = true
	w.mu.Unlock()

	w.runRefresh(ctx, e)

	w.mu.Lock()
	delete(w.inFlight, e.Name)
	pe, queued := w.pending[e.Name]
	delete(w.pending, e.Name)
	w.mu.Unlock()
	if queued && ctx.Err() == nil {
		w.schedule(ctx, pe)
	}
}

// runRefresh runs the injected refresh under a recover guard. A refresh (a full
// rebuild until SC-1274) runs in the debounce goroutine, outside run's recover,
// so an unguarded panic here would crash the daemon and defeat the "no daemon
// crash" guarantee; recovering keeps the poll safety net in charge and lets
// fire's in-flight cleanup always run.
func (w *codenavWatcher) runRefresh(ctx context.Context, e daemon.ProjectEntry) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error().Interface("panic", r).Str("project", e.Name).
				Msg("codenav: index refresh panicked, poll safety net remains active")
		}
	}()
	if ctx.Err() == nil {
		w.refresh(ctx, w.dbPath, e, w.logger)
	}
}

// Close stops the underlying fsnotify watcher. The dispatch goroutine exits when
// its channels close or ctx is cancelled.
func (w *codenavWatcher) Close() error { return w.fsw.Close() }

// refreshOneProject opens a short-lived store and refreshes a single project,
// matching the poll's "own store per pass" discipline so the writer never shares
// a single-conn handle with query readers.
func refreshOneProject(ctx context.Context, dbPath string, e daemon.ProjectEntry, logger zerolog.Logger) {
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Warn().Err(err).Str("project", e.Name).Msg("codenav: open index failed (watcher refresh)")
		return
	}
	defer func() { _ = st.Close() }()
	refreshProject(ctx, st, e, logger)
}
