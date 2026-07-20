package cmddaemon

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/codenav"
	"github.com/gethuman-sh/human/internal/codenav/store"
	"github.com/gethuman-sh/human/internal/daemon"
)

// codenavIndexInterval is how often the daemon refreshes each registered
// project's code-navigation index. The refresh is source-signature-gated, so a
// tick with no source change is nearly free.
const codenavIndexInterval = 5 * time.Minute

// runCodenavIndexLoop keeps the shared code-navigation index fresh for every
// registered project, so agents, worktrees, and the developer's CLI query one
// daemon-owned index instead of each building its own. It indexes once at
// startup, then on codenavIndexInterval, until ctx is cancelled.
func runCodenavIndexLoop(ctx context.Context, reg *daemon.ProjectRegistry, dbPath string, logger zerolog.Logger) {
	indexRegisteredProjects(ctx, reg, dbPath, logger)
	ticker := time.NewTicker(codenavIndexInterval)
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
		res, err := codenav.IndexProject(ctx, st, e.Name, e.Dir, false)
		if err != nil {
			logger.Warn().Err(err).Str("project", e.Name).Msg("codenav: index refresh failed")
			continue
		}
		if !res.Skipped {
			logger.Info().Str("project", e.Name).
				Int("symbols", res.Info.Symbols).
				Dur("elapsed", res.Elapsed).
				Msg("codenav: index refreshed")
		}
	}
}
