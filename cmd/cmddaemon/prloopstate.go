package cmddaemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/internal/agentstate"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/tracker"
)

// prLoopReadRecheckStep/Tries bound the read-after-write race window for the
// PR loop's state-store reads. The sibling comment-thread read
// (listStageSettled, internal/daemon/board_failure.go) was already hardened
// with a bounded backoff because a just-posted marker may not be visible yet
// (SC-1484/SC-2133); the state-store read that decides this loop never got
// the same treatment, which is exactly how SC-2307 read a superseded verdict.
// Package vars so tests can shrink them to keep the suite fast.
// The bound is set by how long a step's write can trail the event that looks like
// its exit, not by how long a write takes. On SC-3613 the reviewer's `Stop`
// arrived at 08:42:32 and its verdict landed at 08:43:39 — 67 seconds later — and
// a 6-second settle concluded "recorded nothing" and reddened a review that went
// on to approve (SC-4026). 100 seconds covers that with margin.
//
// It costs nothing in the ordinary case: a step writes its report as the last
// thing it does, so a real exit is fresh on the first read and never waits. Only
// the pathological read backs off, and there the alternative is a red card a
// person has to clear.
var (
	prLoopReadRecheckStep  = 10 * time.Second
	prLoopReadRecheckTries = 10
)

// readPRReviewVerdict returns the machine reviewer's verdict recorded in
// stage.pr-review, and the settled read that found it. Three states, not two:
// nothing recorded, a record that is not this round's own write (SC-5554: the
// step's agent may be confirmed gone, in which case the round is re-run rather
// than escalated), and a record that IS this round's and would not decode
// (which escalates immediately — see stageRead).
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
// previous round's leftover, not this round's verdict — read.fresh reports
// false and the returned fields are left at their zero value so a caller
// cannot accidentally act on them. The caller (advancePRLoopFunc) maps a
// recorded-but-not-fresh read to PRLoopOutcome.ReviewStale, which the pure
// decider treats as a dead step (re-run) or a racing writer (escalate)
// depending on StepDead.
//
// exit and summary are the exit contract's, read exactly as readDeployFixReport
// reads them. They are not a duplicate of the verdict: the reviewer records both
// in one report, and a reviewer that could not reach the substrate records
// exit "outage" and NO verdict — read as a verdict alone that is an empty
// verdict, which the loop escalates on instead of parking the card (SC-5627).
// summary is what the outage has instead of a verdict, so it is what names the
// unreachable substrate on the card (SC-5592's lesson).
func readPRReviewVerdict(ctx context.Context, project, pmKey string, notBefore time.Time, logger zerolog.Logger) (verdict, head, findings, exit, summary string, read stageRead) {
	var v struct {
		Verdict  string `json:"verdict"`
		Head     string `json:"head"`
		Findings string `json:"findings"`
		Exit     string `json:"exit"`
		Summary  string `json:"summary"`
	}
	read = readStageReportSettled(ctx, project, pmKey, "stage.pr-review", notBefore, &v, logger)
	return v.Verdict, v.Head, v.Findings, v.Exit, v.Summary, read
}

// readPRFixReport loads the fixer's stage.pr-fix report: its exit, the optional
// enumerated directions it recorded on needs-input, a one-line context — the
// deferred findings note, or the summary (always the summary on an outage, whose
// line names the unreachable substrate) — for the options block, the branch-tip
// SHA it left behind (head — fed to the loop's convergence guard), plus the
// settled read that found it. Absent fields stay zero — the loop driver treats
// a missing exit as escalate, or as a dead-step re-run once the step's agent is
// confirmed gone (SC-5554).
//
// notBefore/read follow readPRReviewVerdict's contract, anchored on this
// round's pr-fix-started marker instead.
func readPRFixReport(ctx context.Context, project, pmKey string, notBefore time.Time, logger zerolog.Logger) (exit string, options []daemon.BoardOption, summary, head string, read stageRead) {
	var v struct {
		Exit     string               `json:"exit"`
		Options  []daemon.BoardOption `json:"options"`
		Deferred string               `json:"deferred"`
		Summary  string               `json:"summary"`
		Head     string               `json:"head"`
	}
	read = readStageReportSettled(ctx, project, pmKey, "stage.pr-fix", notBefore, &v, logger)
	// deferred is the findings note the options block leads with, so it wins
	// wherever there are findings to defer. An outage deferred nothing and its one
	// line is the card's face — which substrate was unreachable — so there the
	// summary wins (SC-5627).
	summary = v.Summary
	if v.Exit != string(daemon.ExitOutage) && v.Deferred != "" {
		summary = v.Deferred
	}
	return v.Exit, v.Options, summary, v.Head, read
}

// readDeployFixReport returns the deploy fixer's stage.deploy-fix record: the
// exit ("" when absent), the blocker a needs-human-work stop recorded, the
// one-line summary, and Unconfirmed — whether NO record of THIS round's
// dispatch was found (SC-5554). The summary is what an OUTAGE has instead of a
// blocker — the exit contract gives an outage no blocker — so it is what names
// the unreachable substrate on the card (SC-5592).
//
// notBefore anchors freshness the same way as the loop reads above.
func readDeployFixReport(ctx context.Context, project, pmKey string, notBefore time.Time, logger zerolog.Logger) daemon.DeployFixReport {
	var v struct {
		Exit    string         `json:"exit"`
		Blocker daemon.Blocker `json:"blocker"`
		Summary string         `json:"summary"`
	}
	read := readStageReportSettled(ctx, project, pmKey, "stage.deploy-fix", notBefore, &v, logger)
	// Nothing recorded, or a record from an earlier dispatch: this round confirmed
	// nothing, and the driver re-runs it rather than redding (SC-5554). A record
	// that IS this round's and would not decode is NOT unconfirmed — that fixer
	// reported — so it falls through to the red.
	return daemon.DeployFixReport{
		Exit:        daemon.StageExit(v.Exit),
		Unconfirmed: !read.fresh && !read.unreadable,
		Blocker:     v.Blocker,
		Summary:     v.Summary,
	}
}

// stageRead is what one settled read of a loop step's report found. Three
// states, not two: a record that is not this round's and a record that is this
// round's and will not decode both leave `fresh` false, and the loop must
// answer them oppositely — the first is a write that may never come (re-run the
// round), the second is a step that reported something unreadable (escalate).
// Carried as one value so the three cannot be passed in the wrong order
// (SC-5554).
type stageRead struct {
	// recorded: a record exists, of any freshness.
	recorded bool
	// fresh: the record found was confirmed to be this round's write AND decoded
	// into out. Only then may out be read.
	fresh bool
	// unreadable: it was this round's write and json.Unmarshal rejected it. out
	// may hold half a record — decoding stops at the error — so it is never read.
	unreadable bool
}

// stale reports the record that is not this round's: recorded, not fresh, and
// not a failed decode of this round's own write.
func (r stageRead) stale() bool { return r.recorded && !r.fresh && !r.unreadable }

// readStageReportSettled loads one loop step's JSON report from the agent
// state store into out, re-reading with a bounded backoff while the record it
// finds cannot yet be confirmed as belonging to the round in progress.
//
// notBefore is the round's own started-marker time (SC-2378/AD2): the daemon
// posts a fresh started-marker immediately after the round's agent is launched
// and the reviewer/fixer writes its report only as the very last thing it does,
// so this round's write always has UpdatedAt >= notBefore, while a prior round's
// still-present record has UpdatedAt < notBefore. A launch REFUSED because an
// agent already owns the step posts no started marker at all (SC-4244), so
// notBefore stays at the previous round's marker and the running round's report
// is judged against that older anchor — never stale. A zero notBefore (no anchor
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
func readStageReportSettled(ctx context.Context, project, pmKey, name string, notBefore time.Time, out any, logger zerolog.Logger) stageRead {
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

	recorded := read()
	fresh := recorded && isFresh()
	for try := 0; !fresh && try < prLoopReadRecheckTries-1; try++ {
		select {
		case <-ctx.Done():
			return stageRead{recorded: recorded}
		case <-time.After(prLoopReadRecheckStep):
		}
		recorded = read()
		fresh = recorded && isFresh()
	}

	if !fresh {
		return stageRead{recorded: recorded}
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		logger.Debug().Err(err).Str("pm", pmKey).Str("name", name).Msg("PR loop: unreadable stage report")
		return stageRead{recorded: recorded, unreadable: true}
	}
	return stageRead{recorded: recorded, fresh: true}
}

// recordReviewRound writes a round's blocking findings to the durable findings
// record (SC-5278). The agent state store keeps only the newest report and
// prunes it after two weeks, so it never answered which classes of finding
// recur; this record does, per project, ticket, PR and round. Best effort: a
// record that cannot be written is logged and the loop goes on, because the
// review's verdict — not its archive — is what the loop runs on.
func recordReviewRound(ctx context.Context, rec recall.FindingsRecorder, project, pmKey string, comments []tracker.Comment, findingsText, head string, logger zerolog.Logger) {
	if rec == nil {
		return
	}
	parsed := daemon.ParseFindings(findingsText)
	if len(parsed) == 0 {
		return
	}
	pr, round := daemon.PRLoopNumber(comments), daemon.PRReviewRounds(comments)
	// round is a count of pr-review-started markers seen in comments (SC-5278):
	// it is only zero when the comment thread could not be read at all (the
	// caller's ListComments failed and passed comments as nil), because a
	// verdict this call is ever reached for was itself read from a report a
	// round's own started-marker anchors — a real round is never round zero.
	// Writing anyway would land every such row at pr=0/round=0, colliding
	// under UNIQUE(project,key,pr,round,file,slug) with any other round's
	// leftover and corrupting FindingClassCounts. Refuse rather than guess.
	if round <= 0 {
		logger.Warn().Str("pm", pmKey).Msg("board PR loop: findings not recorded, comment thread unread this round")
		return
	}
	rows := make([]recall.ReviewFinding, 0, len(parsed))
	for _, f := range parsed {
		rows = append(rows, recall.ReviewFinding{
			Project: project, Key: pmKey, PR: pr, Round: round, Head: head,
			File: f.File, Slug: f.Slug, Class: f.Class, Text: f.Text,
		})
	}
	if err := rec.RecordReviewFindings(ctx, rows); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Int("round", round).Msg("board PR loop: findings record not written")
	}
}

// fixDispositionIsFresh reports whether a fixer's report just read may be
// attributed to the newest review round in comments.
//
// exitFresh alone is not enough: it says only that the record postdates the
// fixer's OWN most recent pr-fix-started marker, which stays true of a PRIOR
// round's still-present report on every LATER review exit — until a new
// fixer's write overwrites it. On the review exit of round N>=2 the newest
// pr-fix-started marker is still round N-1's, so round N-1's report reads as
// recorded+fresh while the round being recorded is N. Writing under exitFresh
// alone therefore attributes round N-1's exit to round N's findings — wrong
// every time the loop terminates on round N without a round-N fixer ever
// running (the class-repeat/identity-repeat escalations and the round cap).
//
// The additional guard mirrors the one AdvancePRLoop already trusts the fix
// report under (board_transition.go: `latestPRLoopStage(comments) ==
// PRStageReview`/`PRStageFix`): a disposition is only this round's when the
// fix step is actually the newest loop step, i.e. a fixer ran since the last
// review started (SC-5278).
func fixDispositionIsFresh(comments []tracker.Comment, exitRecorded, exitFresh bool) bool {
	return exitRecorded && exitFresh && daemon.LatestPRLoopStage(comments) == daemon.PRStageFix
}

// recordFixDisposition attaches the fixer's exit and its one-line account to
// the findings of the round it answered — the round whose review dispatched it,
// which is the newest review round in the thread.
func recordFixDisposition(ctx context.Context, rec recall.FindingsRecorder, project, pmKey string, comments []tracker.Comment, exit, note string, logger zerolog.Logger) {
	if rec == nil || exit == "" {
		return
	}
	pr, round := daemon.PRLoopNumber(comments), daemon.PRReviewRounds(comments)
	if err := rec.SetFindingDisposition(ctx, project, pmKey, pr, round, exit, note); err != nil {
		logger.Warn().Err(err).Str("pm", pmKey).Int("round", round).Msg("board PR loop: finding disposition not written")
	}
}
