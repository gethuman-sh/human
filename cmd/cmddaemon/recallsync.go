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
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// RecallSyncInterval is how often the daemon refreshes the searchable ticket
// record after its immediate pass at startup. Exported so tests can shorten it.
var RecallSyncInterval = 10 * time.Minute

// RecallSyncJitter spreads independently started daemons off the same
// wall-clock tick, so N machines do not hit the tracker's API together.
var RecallSyncJitter = 0.2

// RecallFullSyncEvery is how many delta passes go by before one runs as a full
// sync instead. The full pass is what brings in closed history and removes
// tickets that are genuinely gone; the deltas in between keep the record
// current cheaply.
//
// Counted in passes rather than run on a second timer so two syncs can never
// overlap — they share one index and one tracker's rate budget.
var RecallFullSyncEvery = 6

// RunRecallSync keeps the searchable ticket record current without anyone
// running a command.
//
// The record is what every agent consults before starting work, to find out
// whether a problem is already being handled. It was fed only by a hand-run
// `human index` and held nothing for months, so that check answered "nothing
// found" every time — which is how the same problem came to be solved twice
// (SC-2132).
//
// Mostly delta, with an occasional full pass. A delta keeps the record current
// cheaply and never deletes anything; the full pass is what brings in closed
// history and removes tickets that are genuinely gone.
//
// The full pass is only safe unattended because the prune refuses to act on a
// listing it cannot trust — a short response looks exactly like an emptied
// backlog, and Shortcut cannot report that it was capped. Without that guard
// this schedule would delete history it merely failed to fetch, once an hour.
//
// It runs one pass immediately (a restart should not blind the record for a
// full interval) and then on a jittered ticker, mirroring RunBoardReconcile.
func RunRecallSync(ctx context.Context, reg *daemon.ProjectRegistry, resolver *vault.Resolver, dbPath string, interval time.Duration, logger zerolog.Logger) {
	if reg == nil {
		return
	}
	logger.Info().Msg("recall sync started")

	// Startup is always a delta. The daemon re-execs on every rebuild, so a full
	// pass here would mean a complete re-fetch every few minutes during an
	// active session.
	recallSyncOnce(ctx, reg, resolver, dbPath, false, logger)
	for pass := 1; ; pass++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(daemon.JitteredInterval(interval, RecallSyncJitter)):
			full := RecallFullSyncEvery > 0 && pass%RecallFullSyncEvery == 0
			recallSyncOnce(ctx, reg, resolver, dbPath, full, logger)
		}
	}
}

// ticketSources keeps the instances that actually hold this project's tickets.
//
// A configured tracker is not automatically a ticket source: a GitHub entry may
// exist purely for pull requests, and it answers a ticket listing by searching
// every issue the token can see — expensive, unrelated to the record, and rate
// limited. Observed live: the scheduled sync tripped GitHub's secondary rate
// limit every pass while contributing nothing (SC-2132).
//
// A tracker earns a role by carrying this project's pipeline work, so role is
// the signal for "these are our tickets". A team whose tracker IS GitHub sets
// role: pm and is indexed exactly as before.
//
// This compensates for a root cause tracked separately: a githubs: entry always
// registers as BOTH tracker and forge, so there is no way to say "I use GitHub
// only for pull requests" (SC-1671). Until that is fixed the forge-only entry
// looks like a tracker to everything, and this filter keeps it out of the one
// path that would hammer it every ten minutes.
//
// The manual `human index` is deliberately left alone: someone running it by
// hand has said what they want indexed, and may well want a roleless tracker in
// their record. Only the unattended pass is conservative.
func ticketSources(instances []tracker.Instance) []tracker.Instance {
	out := instances[:0:0]
	for _, inst := range instances {
		if inst.InferRole() == "" {
			continue
		}
		out = append(out, inst)
	}
	return out
}

// recallSyncOnce refreshes the record for every registered project. A failure
// for one project or one tracker never stops the others: a stale record is
// recoverable, a loop that died is not.
func recallSyncOnce(ctx context.Context, reg *daemon.ProjectRegistry, resolver *vault.Resolver, dbPath string, full bool, logger zerolog.Logger) {
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
		instances = ticketSources(instances)
		if len(instances) == 0 {
			continue
		}
		// Capture the sync's own prose rather than discarding it: a count of
		// errors with no way to learn what failed is the shape of problem this
		// work exists to remove, and discarding the detail put it right back.
		var detail strings.Builder
		res, sErr := recall.Sync(ctx, store, instances, full, &detail)
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
			Bool("full", full).Str("dir", entry.Dir).Msg("recall sync: refreshed the ticket record")
	}
}
