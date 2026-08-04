package cmddaemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
)

// prLoopReadRecheckStep/Tries bound the read-after-write race window for the
// PR loop's state-store reads. The sibling comment-thread read
// (listStageSettled, internal/daemon/board_failure.go) was already hardened
// with a bounded backoff because a just-posted marker may not be visible yet
// (SC-1484/SC-2133); the state-store read that decides this loop never got
// the same treatment, which is exactly how SC-2307 read a superseded verdict.
// Package vars so tests can shrink them to keep the suite fast.
var (
	prLoopReadRecheckStep  = 2 * time.Second
	prLoopReadRecheckTries = 3
)

// readPRReviewVerdict returns the machine reviewer's verdict recorded in
// stage.pr-review, and whether a report was found at all. Absence is reported
// separately from an empty verdict because they are different failures: a step
// that recorded nothing died or never got that far, while a step that recorded
// something unrecognized decided something the daemon cannot read. Both
// escalate; only the message differs (SC-1892).
//
// The reviewer's report carries non-string fields (a blocking count), so it is
// read into a typed struct with only the needed scalars rather than a
// map[string]string, which json.Unmarshal rejects on the first non-string value.
//
// head is the branch-tip SHA the reviewer actually read (the local ref, not
// origin's — SC-1760). The loop's convergence guard compares it against the head
// the following fix leaves behind, so a fix that adds no commit escalates instead
// of driving an endless re-review.
//
// notBefore anchors freshness (SC-2378): it is this round's own
// pr-review-started marker time (daemon.LatestMarkerTime), obtained by the
// caller from the comment thread. A record whose UpdatedAt predates it is a
// previous round's leftover, not this round's verdict — fresh reports false
// and the returned fields are left at their zero value so a caller cannot
// accidentally act on them. The caller (advancePRLoopFunc) maps a recorded-
// but-not-fresh read to PRLoopOutcome.ReviewStale, which the pure decider
// escalates on rather than trusting.
func readPRReviewVerdict(ctx context.Context, project, pmKey string, notBefore time.Time, logger zerolog.Logger) (verdict, head string, recorded, fresh bool) {
	var v struct {
		Verdict string `json:"verdict"`
		Head    string `json:"head"`
	}
	recorded, fresh = readStageReportSettled(ctx, project, pmKey, "stage.pr-review", notBefore, &v, logger)
	return v.Verdict, v.Head, recorded, fresh
}

// readPRFixReport loads the fixer's stage.pr-fix report: its exit, the optional
// enumerated directions it recorded on needs-input, a one-line context (deferred
// comments, else the summary) for the options block, the branch-tip SHA it left
// behind (head — fed to the loop's convergence guard), plus whether a report was
// found at all. Absent fields stay zero — the loop driver treats a missing exit
// as escalate.
//
// notBefore/fresh follow readPRReviewVerdict's contract, anchored on this
// round's pr-fix-started marker instead.
func readPRFixReport(ctx context.Context, project, pmKey string, notBefore time.Time, logger zerolog.Logger) (exit string, options []daemon.BoardOption, summary, head string, recorded, fresh bool) {
	var v struct {
		Exit     string               `json:"exit"`
		Options  []daemon.BoardOption `json:"options"`
		Deferred string               `json:"deferred"`
		Summary  string               `json:"summary"`
		Head     string               `json:"head"`
	}
	recorded, fresh = readStageReportSettled(ctx, project, pmKey, "stage.pr-fix", notBefore, &v, logger)
	summary = v.Deferred
	if summary == "" {
		summary = v.Summary
	}
	return v.Exit, v.Options, summary, v.Head, recorded, fresh
}

// readDeployFixExit returns the deploy fixer's exit recorded in stage.deploy-fix
// ("" when absent — the driver treats a non-done exit, including absence, as red).
//
// notBefore anchors freshness the same way as the loop reads above.
func readDeployFixExit(ctx context.Context, project, pmKey string, notBefore time.Time, logger zerolog.Logger) daemon.StageExit {
	var v struct {
		Exit string `json:"exit"`
	}
	_, _ = readStageReportSettled(ctx, project, pmKey, "stage.deploy-fix", notBefore, &v, logger)
	// The state store hands back a bare string; this is the one place it becomes
	// a StageExit, so the parse boundary is explicit rather than implied.
	return daemon.StageExit(v.Exit)
}

// readStageReportSettled loads one loop step's JSON report from the agent
// state store into out, re-reading with a bounded backoff while the record it
// finds cannot yet be confirmed as belonging to the round in progress.
//
// notBefore is the round's own started-marker time (SC-2378/AD2): the daemon
// posts a fresh started-marker at the beginning of every round and the
// reviewer/fixer writes its report only as the very last thing it does, so
// this round's write always has UpdatedAt >= notBefore, while a prior round's
// still-present record has UpdatedAt < notBefore. A zero notBefore (no anchor
// available) treats any record found as fresh, matching the old unconditional
// behaviour.
//
// recorded reports whether a report was found at all, of any freshness — a
// step that never wrote anything is a different failure than one whose write
// merely has not settled yet, and the caller (and the escalation message)
// needs to tell them apart. fresh reports whether the record found was
// confirmed to be this round's; out is populated only when fresh, never on a
// stale or unparseable record, so a caller cannot accidentally read stale
// fields believing them current.
func readStageReportSettled(ctx context.Context, project, pmKey, name string, notBefore time.Time, out any, logger zerolog.Logger) (recorded, fresh bool) {
	var raw string
	var updatedAt time.Time
	read := func() bool {
		err := withStateStore(func(store agentstate.Store) error {
			entry, err := store.Get(ctx, project, pmKey, name)
			if err != nil {
				return err
			}
			raw, updatedAt = entry.Value, entry.UpdatedAt
			return nil
		})
		if err != nil {
			logger.Debug().Err(err).Str("pm", pmKey).Str("name", name).Msg("PR loop: no readable stage report")
			return false
		}
		return true
	}
	isFresh := func() bool { return !updatedAt.Before(notBefore) }

	recorded = read()
	fresh = recorded && isFresh()
	for try := 0; !fresh && try < prLoopReadRecheckTries-1; try++ {
		select {
		case <-ctx.Done():
			return recorded, fresh
		case <-time.After(prLoopReadRecheckStep):
		}
		recorded = read()
		fresh = recorded && isFresh()
	}

	if !fresh {
		return recorded, fresh
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		logger.Debug().Err(err).Str("pm", pmKey).Str("name", name).Msg("PR loop: unreadable stage report")
		return recorded, false
	}
	return recorded, fresh
}
