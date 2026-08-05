package cmddaemon

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/cmd/cmdutil"
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

	recallSyncLoop(ctx, interval, RecallFullSyncEvery, func(full bool) {
		recallSyncOnce(ctx, reg, resolver, dbPath, full, logger)
	})
}

// recallSyncLoop drives the schedule: an immediate pass, then one per interval,
// with every fullEvery-th pass running full.
//
// Separated from the work it drives so the schedule itself can be exercised —
// timing wrapped around a function that opens a database and calls a tracker is
// otherwise only testable by re-implementing the arithmetic in the test, which
// proves nothing about the loop.
//
// The first pass is never full: the daemon re-execs on every rebuild, so a full
// pass at startup would mean a complete re-fetch every few minutes during an
// active session. A non-positive fullEvery disables full passes entirely.
func recallSyncLoop(ctx context.Context, interval time.Duration, fullEvery int, pass func(full bool)) {
	pass(false)
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(daemon.JitteredInterval(interval, RecallSyncJitter)):
			pass(fullEvery > 0 && n%fullEvery == 0)
		}
	}
}

// ticketSources keeps the instances that actually hold this project's tickets.
//
// A configured GitHub entry is not automatically a ticket source: it may exist
// purely for pull requests, and it answers a ticket listing by searching every
// issue the token can see — expensive, unrelated to the record, and rate
// limited. Observed live: the scheduled sync tripped GitHub's secondary rate
// limit every pass while contributing nothing (SC-2132).
//
// An entry that DECLARES itself a forge is already excluded further in, where
// recall.Sync drops everything that is not a tracker (SC-1671). What is left is
// the legacy shape that split predates: a bare githubs: entry with no role,
// which still registers as a tracker and looks like one to every caller. A role
// is what tells the two apart, so a roleless GitHub entry stays out of the
// unattended pass. A team whose tracker IS GitHub sets role: pm and is indexed
// exactly as before.
//
// Confined to GitHub on purpose. Every other backend is configured because
// someone keeps tickets in it, and only Shortcut infers a role for free — so
// skipping roleless trackers in general would keep a Linear or Jira backlog out
// of the record entirely, which is the failure this work exists to remove.
//
// The manual `human index` is deliberately left alone: someone running it by
// hand has said what they want indexed, and may well want a roleless tracker in
// their record. Only the unattended pass is conservative.
func ticketSources(instances []tracker.Instance) []tracker.Instance {
	out := instances[:0:0]
	for _, inst := range instances {
		if inst.Kind == "github" && inst.InferRole() == "" {
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
		// cost the others their refresh (SC-2005). logReportableLoadFailures skips
		// the held-off ones the vault's own backoff already reported once, so a
		// standing outage does not re-log on every 10m sync (SC-3322).
		instances, failures := cmdutil.LoadAllInstancesTolerant(entry.Dir, entry.EnvLookup(), resolver)
		logReportableLoadFailures(failures, entry.Dir)
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
