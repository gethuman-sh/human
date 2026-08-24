package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// BoardFreshnessInterval is how often the daemon re-lists open tickets to catch
// changes made OUTSIDE the board — the tracker web UI, the CLI, a teammate, or
// another daemon — none of which flow through a route handler and so raise no
// board event. It is shorter than the desktop's own ~90s safety poll so such a
// change surfaces in seconds rather than up to a minute and a half.
var BoardFreshnessInterval = 30 * time.Second

// BoardFreshnessJitter spreads the poll by up to ±this fraction of the interval
// so several daemons watching one board do not list the tracker in lockstep.
var BoardFreshnessJitter = 0.2

// IssueLister enumerates the open tickets whose set the freshness poll
// fingerprints. Its signature matches the server's LiteIssueFetcher so the
// cheap titles-only listing is reused (no per-ticket comment scan).
type IssueLister func() ([]TrackerIssuesResult, error)

// BoardFreshnessOpts is what the poll needs. A struct rather than a seventh
// positional parameter: the observer is optional and unrelated to the poke, and
// a call site that has to pass nil in the middle of six positions is where the
// next argument goes in the wrong slot.
type BoardFreshnessOpts struct {
	List        IssueLister
	Poke        func()
	HasWatchers func() bool
	// Observe, when set, is handed every successful listing — the same one the
	// fingerprint is taken from, so watching for a per-ticket change costs no
	// extra tracker traffic. Called on every tick with watchers, including the
	// baseline tick.
	Observe  func([]TrackerIssuesResult)
	Interval time.Duration
	Logger   zerolog.Logger
}

// RunBoardFreshnessPoll re-lists open tickets on an interval and pokes open
// board subscribers whenever the ticket set changes — a new or removed ticket,
// or an in-place edit reflected in a ticket's UpdatedAt. It is the daemon-side
// counterpart to the board's event-driven refresh: mutations made THROUGH the
// board already poke via the route handlers, so this exists solely to cover
// everything made outside it.
//
// The poll only does tracker work while at least one UI is subscribed
// (HasWatchers): the signal is meaningless with no board open, and gating on it
// bounds tracker API load to the times a board is actually being watched. The
// first list after watchers appear establishes the baseline silently — the UI's
// own initial fetch already holds that state, so poking for it would trigger a
// redundant refetch. A nil lister or poke disables the loop (tests, disabled
// tracking).
//
// The listing it already paid for is handed to Observe, which is how a
// per-ticket watcher (the idea drafter's redraft trigger) sees tracker-side
// changes without a second listing of its own — and therefore inside the same
// HasWatchers gate. A change made to an idea while no board is open raises its
// redraft at the first tick after one opens, not at the moment of the edit.
// That is the deliberate trade: the alternative is listing the tracker around
// the clock on every machine running a daemon, to write a draft nobody is
// waiting for. Ideas captured on the board — the case the feature exists for —
// draft immediately from their own launch at capture, not from this poll.
func RunBoardFreshnessPoll(ctx context.Context, opts BoardFreshnessOpts) {
	if opts.List == nil || opts.Poke == nil || opts.HasWatchers == nil {
		return
	}
	var st freshnessState
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(JitteredInterval(opts.Interval, BoardFreshnessJitter)):
		}
		if !opts.HasWatchers() {
			// Drop the baseline so the next watcher re-baselines against live
			// state instead of poking for a change it never saw a "before" for.
			st.haveBaseline = false
			continue
		}
		results, err := opts.List()
		if err != nil {
			opts.Logger.Debug().Err(err).Msg("board freshness poll: listing tickets failed; retrying next tick")
			continue
		}
		if opts.Observe != nil {
			opts.Observe(results)
		}
		if st.step(fingerprintIssues(results)) {
			opts.Poke()
			opts.Logger.Debug().Msg("board freshness poll: ticket set changed outside the board; poking subscribers")
		}
	}
}

// freshnessState carries the last-seen fingerprint across poll ticks. Split from
// the loop so the poke decision is unit-testable without timers.
type freshnessState struct {
	baseline     string
	haveBaseline bool
}

// step folds one tick's fingerprint into the baseline and reports whether
// subscribers should be poked. Split from the loop so the poke decision is
// unit-testable without timers.
func (st *freshnessState) step(fp string) bool {
	if !st.haveBaseline {
		st.baseline = fp
		st.haveBaseline = true
		return false
	}
	if fp != st.baseline {
		st.baseline = fp
		return true
	}
	return false
}

// fingerprintIssues renders an order-independent digest of the open-ticket set.
// It folds in every field a board card reflects — key, title, status, type and
// labels (kind) — plus UpdatedAt, the tracker's own "something changed"
// timestamp, so an in-place edit is caught even when set membership is
// unchanged. Over-detecting is harmless (a redundant refetch); under-detecting
// would leave the board stale, so the digest errs toward sensitivity.
func fingerprintIssues(results []TrackerIssuesResult) string {
	var lines []string
	for i := range results {
		for j := range results[i].Issues {
			is := results[i].Issues[j]
			labels := append([]string(nil), is.Labels...)
			sort.Strings(labels)
			// \x1f (unit separator) cannot occur in these fields, so it delimits
			// them unambiguously; keys are unique per issue.
			fields := []string{
				is.Key, is.Title, is.Status, is.Type,
				strconv.FormatInt(is.UpdatedAt.UnixNano(), 10),
				strings.Join(labels, ","),
			}
			lines = append(lines, strings.Join(fields, "\x1f"))
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}
