package cmddaemon

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/vault"
)

// RecallSyncInterval is how often the daemon refreshes the searchable ticket
// record after its immediate pass at startup. Exported so tests can shorten it.
var RecallSyncInterval = 10 * time.Minute

// RecallSyncJitter spreads independently started daemons off the same
// wall-clock tick, so N machines do not hit the tracker's API together.
var RecallSyncJitter = 0.2

// RunRecallSync keeps the searchable ticket record current without anyone
// running a command.
//
// The record is what every agent consults before starting work, to find out
// whether a problem is already being handled. It was fed only by a hand-run
// `human index` and held nothing for months, so that check answered "nothing
// found" every time — which is how the same problem came to be solved twice
// (SC-2132).
//
// DELTA ONLY, never a full sync. That is what makes it safe to run unattended:
// recall.Sync prunes any key it did not see this pass, and until the prune is
// guarded against a truncated listing, an unattended full sync could delete
// history it merely failed to fetch. A delta pass skips the prune entirely once
// a source has any entries, and it still carries the goal — a ticket closing is
// an update, so closed tickets flow in as they close.
//
// It runs one pass immediately (a restart should not blind the record for a
// full interval) and then on a jittered ticker, mirroring RunBoardReconcile.
func RunRecallSync(ctx context.Context, reg *daemon.ProjectRegistry, resolver *vault.Resolver, dbPath string, interval time.Duration, logger zerolog.Logger) {
	if reg == nil {
		return
	}
	logger.Info().Msg("recall sync started")

	recallSyncOnce(ctx, reg, resolver, dbPath, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(daemon.JitteredInterval(interval, RecallSyncJitter)):
			recallSyncOnce(ctx, reg, resolver, dbPath, logger)
		}
	}
}

// recallSyncOnce refreshes the record for every registered project. A failure
// for one project or one tracker never stops the others: a stale record is
// recoverable, a loop that died is not.
func recallSyncOnce(ctx context.Context, reg *daemon.ProjectRegistry, resolver *vault.Resolver, dbPath string, logger zerolog.Logger) {
	store, err := recall.NewSQLiteStore(dbPath)
	if err != nil {
		logger.Warn().Err(err).Msg("recall sync: cannot open the index")
		return
	}
	defer func() { _ = store.Close() }()

	for _, entry := range reg.Entries() {
		// Tolerant loading so one tracker's momentary credential failure does not
		// cost the others their refresh (SC-2005).
		instances, failures := cmdutil.LoadAllInstancesTolerant(entry.Dir, entry.EnvLookup(), resolver)
		for _, failure := range failures {
			errors.LogError(failure).Str("dir", entry.Dir).
				Msg("recall sync: tracker instances failed to load, continuing without them")
		}
		if len(instances) == 0 {
			continue
		}
		// Capture the sync's own prose rather than discarding it: a count of
		// errors with no way to learn what failed is the shape of problem this
		// work exists to remove, and discarding the detail put it right back.
		var detail strings.Builder
		res, sErr := recall.Sync(ctx, store, instances, false, &detail)
		if sErr != nil {
			logger.Warn().Err(sErr).Str("dir", entry.Dir).Str("detail", detail.String()).
				Msg("recall sync: failed")
			continue
		}
		event := logger.Info()
		if res.Errors > 0 {
			// The count alone is unactionable; the prose names the tracker and
			// the ticket that failed.
			event = logger.Warn().Str("detail", strings.TrimSpace(detail.String()))
		}
		event.Int("indexed", res.Indexed).Int("errors", res.Errors).Int("pruned", res.Pruned).
			Str("dir", entry.Dir).Msg("recall sync: refreshed the ticket record")
	}
}
