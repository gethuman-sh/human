package cmddaemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/codenav/store"
	"github.com/gethuman-sh/human/internal/daemon"
)

// codenavPollInterval is the poll cadence when the file watcher is unavailable
// (degraded, poll-only mode) — the historical safety cadence. The refresh is
// source-signature-gated, so a tick with no source change is nearly free.
const codenavPollInterval = 5 * time.Minute

// codenavPollSafetyNetInterval is the backed-off poll cadence once the file
// watcher carries freshness. The poll then only catches changes a watcher can
// miss (network/container FS, event overflow, bulk operations).
const codenavPollSafetyNetInterval = 30 * time.Minute

// pollInterval selects the poll cadence from whether the watcher is active, so
// "back off the poll once the watcher carries freshness" and "degrade to the
// primary poll when the watcher cannot run" are one testable branch.
func pollInterval(watcherActive bool) time.Duration {
	if watcherActive {
		return codenavPollSafetyNetInterval
	}
	return codenavPollInterval
}

// runCodenavIndexLoop keeps the shared code-navigation index fresh for every
// registered project, so agents, worktrees, and the developer's CLI query one
// daemon-owned index instead of each building its own. It indexes once at
// startup, starts a debounced file watcher for near-real-time freshness, then
// polls as a safety net until ctx is cancelled. If the watcher cannot start the
// poll stays at the primary cadence so freshness never depends on the watcher.
func runCodenavIndexLoop(ctx context.Context, reg *daemon.ProjectRegistry, dbPath string, logger zerolog.Logger) {
	indexRegisteredProjects(ctx, reg, dbPath, logger)

	watcherActive := false
	if w, err := startCodenavWatcher(ctx, reg, dbPath, logger); err == nil {
		watcherActive = true
		defer func() { _ = w.Close() }()
	} else {
		logger.Warn().Err(err).Msg("codenav: file watcher unavailable, falling back to poll-only mode")
	}

	ticker := time.NewTicker(pollInterval(watcherActive))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			indexRegisteredProjects(ctx, reg, dbPath, logger)
		}
	}
}

// indexRegisteredProjects refreshes the index for every registered project once.
// A per-project failure is logged and skipped so one bad repo never stalls the
// rest; a failed db open aborts the pass (next tick retries). It opens its own
// store.Store so it never shares a single-conn handle with query handling, which
// would otherwise serialize the writer against concurrent readers.
func indexRegisteredProjects(ctx context.Context, reg *daemon.ProjectRegistry, dbPath string, logger zerolog.Logger) {
	st, err := store.Open(dbPath)
	if err != nil {
		logger.Warn().Err(err).Msg("codenav: open index failed")
		return
	}
	defer func() { _ = st.Close() }()
	for _, e := range reg.Entries() {
		refreshProject(ctx, st, e, logger)
	}
}

// refreshProject refreshes one project's index against an already-open store.
// A failure is logged and swallowed so one bad repo never stalls the caller.
// This is the single refresh entry point shared by the poll and the watcher, so
// the two paths never drift (and both inherit incremental refresh once SC-1274
// lands, with no change here).
func refreshProject(ctx context.Context, st *store.Store, e daemon.ProjectEntry, logger zerolog.Logger) {
	res, err := codenav.IndexProject(ctx, st, e.Name, e.Dir, false)
	if err != nil {
		logger.Warn().Err(err).Str("project", e.Name).Msg("codenav: index refresh failed")
		return
	}
	if !res.Skipped {
		logger.Info().Str("project", e.Name).
			Int("symbols", res.Info.Symbols).
			Dur("elapsed", res.Elapsed).
			Msg("codenav: index refreshed")
	}
}
