package daemon

import (
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// BoardCard is the derived per-PM placement on the pipeline board. It is the
// single source of truth shared on the wire with the GUI and TUI, so neither
// re-derives from raw comments.
type BoardCard struct {
	Stage          BoardStage `json:"stage"`
	State          BoardState `json:"state"`
	EngineeringKey string     `json:"engineering_key,omitempty"`
	Branch         string     `json:"branch,omitempty"`
	// Commits is the `commits:` line of the latest [human:ready-for-review]
	// handoff — the exact SHAs under review. It rides the card so the daemon can
	// hard-bind a dispatched reviewer to the handed-off work rather than letting
	// it free-associate from whatever HEAD its worktree sits on (SC-695).
	Commits string `json:"commits,omitempty"`
	PRURL   string `json:"pr_url,omitempty"`
	Error   string `json:"error,omitempty"`
	// ResumeAt is the RFC3339 instant a paused (outage) card's standing marker
	// names as when the substrate is stated to clear (a "resume:" line — see
	// pausedOutageBody), when one was parsed out of the diagnosis. Empty when
	// the outage carries no stated recovery time, in which case the wait falls
	// back to the reconcile pass's own backoff exactly as before (SC-3024).
	ResumeAt string `json:"resume_at,omitempty"`
	// WaitsFor is the ticket a queued card was told to wait for: the `waits-for`
	// field of the [human:option-chosen] marker that queued it, written when the
	// answer a human picked was a sequencing one. Empty on every other card,
	// including a queued one whose answer was an ordinary direction — that card is
	// waiting for a launch, not for other work.
	WaitsFor string `json:"waits_for,omitempty"`
	// HasPlan reports a [human:plan] comment on the ticket — the plan lives
	// here instead of on a separate engineering ticket (single-tracker
	// topology).
	HasPlan bool `json:"has_plan,omitempty"`
	// HasRelatedRecord reports a COMPLETED filing-time related-work record
	// ([human:related] found/none) on the ticket. The frontend uses it to
	// suppress the on-demand "Find related work" card menu item — an incomplete
	// record does not set it, so a died-halfway run stays re-runnable (SC-2405).
	HasRelatedRecord bool `json:"has_related_record,omitempty"`
	// TBACount is how many unanswered [TBA:] gaps the idea's drafted
	// description still carries (SC-4608). Zero on every non-idea card and on
	// an idea nothing has drafted yet; the card face renders nothing for zero.
	TBACount int `json:"tba_count,omitempty"`
	// Verdict is the review verdict that still governs the card: the `verdict:`
	// line of the latest [human:review-complete] comment, unless a newer
	// [human:ready-for-review] handoff has answered it — a verdict judges the
	// round it read, so a rework handoff retires it and the field goes empty
	// (SC-4958). A fail or incomplete verdict keeps the card out of Ready to
	// Deploy and blocks the deploy transition; an absent verdict counts as pass
	// so threads reviewed before verdicts existed keep flowing.
	Verdict string `json:"verdict,omitempty"`
	// ShippedPartial reports a [human:shipped-partial] marker on the ticket: the
	// planner's sanctioned ship-narrow-plus-follow-on fork left one or more
	// acceptance criteria to a follow-on ticket, so this card shipped less than
	// the ticket asked (SC-2910). Derived from the marker the way Verdict is
	// derived from [human:review-complete]; absent on every card with no such
	// marker, so an ordinary card renders exactly as before.
	ShippedPartial bool `json:"shipped_partial,omitempty"`
	// ShippedPartialFollowOn is the `follow-on` field of that marker — the real
	// ticket key that now carries the deferred criteria, so the card can name and
	// link it. Empty when ShippedPartial is false.
	ShippedPartialFollowOn string `json:"shipped_partial_follow_on,omitempty"`
	// Options is the latest unconsumed [human:options] block: a stage ended
	// in a decision and the card is waiting for a human to pick a direction.
	// Consumed (cleared) by an option-chosen comment or any later
	// stage-started marker.
	Options        []BoardOption `json:"options,omitempty"`
	OptionsContext string        `json:"options_context,omitempty"`
	OptionsStage   BoardStage    `json:"options_stage,omitempty"`
	// StopDecision is the head of the OPERATIVE ticket-review stop verdict
	// (superseded/escalated/rejected) — the pre-planning gate decided the ticket
	// must not proceed and nothing has superseded that verdict. Empty for every
	// other card (undecided, advancing, or re-dispatched), so a card without a
	// decision renders exactly as before. The frontend maps the head to human
	// phrasing (STOP_DECISION_LABELS), mirroring RUNNING_LABELS — the daemon
	// carries the datum, not the copy.
	StopDecision string `json:"stop_decision,omitempty"`
	// StopLinkedKey is the other ticket the decision names: the parent that
	// carries the work (superseded) or the design ticket created to unblock it
	// (escalated), read from the marker's `linked:` field. Empty for rejected and
	// for any decision that named none.
	StopLinkedKey string `json:"stop_linked_key,omitempty"`
	// StopReasoning is the recorded body of that verdict — the evidence the gate
	// wrote for why it stopped, so the reason is readable from the card without
	// opening the tracker.
	StopReasoning string `json:"stop_reasoning,omitempty"`
	// ResolvedReason is WHY a planning card ended with nothing to plan — the
	// `reason:` field of the operative [human:nothing-to-do] marker (merged,
	// duplicate, escalated or rejected). One terminal state stands for four
	// determinations, and a board that labelled every one of them "already
	// shipped" told a person a refused ticket's work existed (SC-5326). Empty on
	// every other card, and on a record posted before the field was required, so
	// those render as an unlabelled resolution rather than as shipped.
	ResolvedReason string `json:"resolved_reason,omitempty"`
	// StageEnteredAt is the Created time of the newest marker in the card's
	// current stage — for a plan-done card, when the current plan landed. The
	// board renders it as an age badge so work rotting in a queue is visible.
	StageEnteredAt time.Time `json:"stage_entered_at,omitzero"`
	// StageRunStartedAt is the Created time of the newest "-started" marker in
	// the card's current stage — the boundary between phase records (stage.fix,
	// stage.verify, …) the CURRENT run itself wrote and whatever an earlier,
	// unrelated run left in the same per-ticket-key agentstate store. Those
	// records accumulate for the ticket's whole life with no clearing path
	// (agentstate.DefaultRetention is 14d), so without a lower bound a stage
	// reaped or crashed before writing anything borrows a PREVIOUS run's phase
	// and asserts it in the past tense about a run that never reached it
	// (SC-3656 PR review finding). For a running card this coincides with
	// StageEnteredAt (the started marker IS the newest marker in the stage);
	// for an ended (failed/resolved) card it is earlier, bounding in the run
	// that actually produced the outcome. Zero when the stage carries no
	// started marker at all.
	StageRunStartedAt time.Time `json:"stage_run_started_at,omitzero"`
	// StageDaemonID is the posting daemon signed onto that same deciding marker
	// (the machine: field, read via ParseDaemonID). It tells the durable
	// stuck-running reconcile pass which daemon owns a running stage, so a peer
	// daemon spares a live foreign-owned card instead of reddening work it simply
	// cannot see locally (SC-1450). Empty for an unsigned marker, preserving
	// single-daemon behaviour.
	StageDaemonID string `json:"stage_daemon_id,omitempty"`
	// RunningStage names a stage OTHER than this card's own placement whose own
	// newest marker is a start — an agent may still be working the ticket while
	// the card shows a failure somewhere else. Set only on a FAILED card, which
	// is the only placement whose reading it changes: a card carries one (stage,
	// state) pair and the newest marker wins, so when two stages interleave the
	// live one becomes invisible and the red asks a person to intervene on work
	// the machine is still doing (SC-4406, measured on SC-3853).
	//
	// It widens WHICH agent the viewer asks about, never what it concludes: the
	// liveness answer still comes from a running container, so a stale running
	// marker with nothing behind it reds the card exactly as before.
	RunningStage BoardStage `json:"running_stage,omitempty"`
	// DeployPhase names the done-stage sub-phase for a running card: "pr-review"
	// while the machine reviewer runs, "pr-fix" while the fixer runs, empty for
	// a plain deploy. It lets the board badge read "PR review…"/"fixing PR
	// review findings…" instead of "deploying…" so the loop is visible while it
	// runs.
	DeployPhase string `json:"deploy_phase,omitempty"`
	// PRReviewRound is which round of the pre-merge review→fix loop a RUNNING
	// done-stage card is in, and PRReviewRoundCap the outer bound it runs
	// against (DefaultPRReviewRounds). Both are zero on every other card, and
	// zero renders exactly as the card rendered before.
	//
	// The two loop halves alternate their badge word, so a card at round one and
	// a card at round seven read identically: alternating labels can show motion,
	// only the round can show the motion is circular. The number is not new —
	// prReviewRounds is the counter the loop's own outer bound reads — it has
	// simply never left the daemon.
	PRReviewRound    int `json:"pr_review_round,omitempty"`
	PRReviewRoundCap int `json:"pr_review_round_cap,omitempty"`
	// Degraded marks a card whose comment thread could not be read this scan
	// (a ListComments error/timeout). It is set at the fetch-error site, never
	// by DeriveBoardCard (which only runs on a successful fetch). Stage/State
	// carry the last-known placement when available so the board renders the
	// card locked in place rather than silently demoting it to Backlog (1700).
	Degraded bool `json:"degraded,omitempty"`
}

// VerdictFailed reports whether a review verdict blocks the card from moving
// forward. A "fail" (the code was examined and found wanting) blocks, and so
// does an "incomplete" — built correctly, but not everything the ticket asked
// for: one or more acceptance criteria unmet. Both keep the card out of Ready
// to Deploy and drive the rework loop; absence is not failure, so pre-verdict
// threads keep flowing (SC-2848).
func VerdictFailed(verdict string) bool {
	v := strings.ToLower(strings.TrimSpace(verdict))
	return strings.HasPrefix(v, "fail") || strings.HasPrefix(v, "incomplete")
}

// DeriveBoardCard computes a PM ticket's board placement from its comment
// thread and tracker status. A closed/done ticket is always Hidden — closing
// is how work leaves the board, whatever its pipeline history. For open
// tickets the rule: the furthest stage carrying ANY marker wins; within that
// stage the latest marker (by Created) decides running/done/failed. A ticket
// with no markers sits in Backlog. Pure: no I/O.
//
// isIdea (the ticket carries an idea label, tracker.Issue.IsIdea) takes
// precedence over everything while the ticket is open: an idea sits in the
// Ideas column even if it somehow carries pipeline markers — deliberately, so
// the label is the single source of truth until promotion removes it.
func DeriveBoardCard(comments []tracker.Comment, statusType tracker.Category, isIdea bool) BoardCard {
	// The lister normally filters closed tickets, but one closed mid-session
	// (the board's own Close action, or a teammate on the tracker) can still
	// arrive here via an in-flight fetch — it must never render as open work.
	if statusType == tracker.CategoryDone || statusType == tracker.CategoryClosed {
		return cardAt(atStage(BoardHidden))
	}

	if isIdea {
		return cardAt(atStage(BoardIdeas))
	}

	furthest := BoardBacklog
	furthestRank := -1
	var anyMarker bool

	// First pass: find the furthest stage that any marker reaches.
	for _, c := range comments {
		p, ok := fromMarker(c.Body)
		if !ok {
			continue
		}
		anyMarker = true
		if r := stageRank[p.Stage()]; r > furthestRank {
			furthestRank = r
			furthest = p.Stage()
		}
	}

	_, hasPlan := latestPlanComment(comments)
	hasRelated := hasCompletedRelatedRecord(comments)

	placed := atStage(furthest)
	var latest tracker.Comment
	if anyMarker {
		// Second pass: within the furthest stage, the latest marker decides state.
		var state BoardState
		state, latest = latestStateInStage(comments, furthest)
		placed = placed.inStage(state)

		// A furthest-stage failure is authoritative only while it is the ticket's
		// newest marker. A strictly-newer marker anywhere — a re-implementation
		// restarting from an earlier stage (ticket 881) or a later deploy — retires
		// the stale red; the card follows the ticket's current activity rather than a
		// terminal failure the pipeline already moved past (SC-910).
		if supersededByNewerMarker(placed, comments) {
			if newest, ok := latestMarkerOverall(comments); ok && commentNewer(newest.comment, latest) {
				placed, latest = placed.supersededBy(newest.placement), newest.comment
			}
		}
	}

	placed, latest, anyMarker = applyStateOverrides(comments, placed, latest, anyMarker)

	if !anyMarker {
		// No pipeline activity yet: the open ticket waits in Backlog.
		card := cardAt(atStage(BoardBacklog))
		card.HasPlan, card.HasRelatedRecord = hasPlan, hasRelated
		return card
	}

	card := cardAt(placed)
	card.HasPlan, card.HasRelatedRecord = hasPlan, hasRelated
	card.StageEnteredAt, card.StageDaemonID = latest.Created, ParseDaemonID(latest.Body)
	// card.Stage, not furthest: a superseded placement (SC-910) can move the
	// card to a stage other than the one stageRank found furthest, and the
	// boundary must track wherever `latest` — and so StageEnteredAt — actually
	// landed, or it would bound the search to a stage the card no longer sits in.
	card.StageRunStartedAt = latestRunStartInStage(comments, card.Stage)
	card.EngineeringKey = firstEngineeringKey(comments)
	card.Branch = latestPrefixedLine(comments, ReadyForReviewHeader, "branch:")
	card.Commits = latestPrefixedLine(comments, ReadyForReviewHeader, "commits:")
	card.Verdict = currentVerdict(comments)
	card.PRURL = derivePRURL(comments)
	if followOn, ok := deriveShippedPartial(comments); ok {
		card.ShippedPartial = true
		card.ShippedPartialFollowOn = followOn
	}
	attachFailureAndResume(&card, card.placement().State(), latest)
	// Only a queued card can be held: the record that holds it is the same
	// option-chosen marker the queued placement was synthesized from, so the
	// moment a started marker supersedes the choice the card stops claiming to
	// wait for anything.
	if card.placement().State() == BoardQueued {
		card.WaitsFor = waitsForOf(latest)
	}
	card.DeployPhase = deployPhaseFor(card, comments)
	card.PRReviewRound, card.PRReviewRoundCap = prLoopRoundFor(card, comments)
	card.RunningStage = runningStageElsewhere(comments, card.placement())
	card.StopDecision, card.StopLinkedKey, card.StopReasoning = ticketReviewStop(latest)
	card.ResolvedReason = nothingToDoReason(latest)
	attachOpenOptions(&card, comments)
	return card
}

// attachFailureAndResume sets the one-line reason and (for a paused outage)
// the stated resume instant, mirroring how a failed card's reason is derived.
// An outage card carries the same one-line reason a failed card does (the
// substrate that was unreachable), so the badge can say WHAT is down, not just
// that it is — the outage marker's body is composed exactly like a failure's
// (SC-2307). Split out of DeriveBoardCard purely to keep that function's
// cyclomatic complexity under the project's gate; ResumeAt is SC-3024.
func attachFailureAndResume(card *BoardCard, state BoardState, latest tracker.Comment) {
	if state != BoardFailed && state != BoardOutage {
		return
	}
	card.Error = failureReason(latest.Body)
	if state == BoardOutage {
		card.ResumeAt = parseResumeLine(latest.Body)
	}
}

// runningStageElsewhere names the stage whose own newest marker is a start
// while the card itself is red somewhere else — the ticket's other, possibly
// live, run. Empty for every card that is not failed, and for a failure with no
// such stage.
//
// Only the three stages that launch a named agent are asked about, because the
// answer exists to be joined against a container name (AgentNamesForCard): the
// done stage runs its PR-loop halves under DeployPhase and a plain deploy runs
// in-process with no agent at all, so neither has a name this could look for.
//
// The card's own stage is skipped rather than trusted to be excluded by its
// failure: a placement can be handed to a card by supersession or a terminal
// determination, and a rule that only holds because of how the placement was
// reached is one edit away from not holding.
func runningStageElsewhere(comments []tracker.Comment, placed Placement) BoardStage {
	if placed.State() != BoardFailed {
		return ""
	}
	var found BoardStage
	var newest tracker.Comment
	for _, stage := range agentLaunchStages {
		if stage == placed.Stage() {
			continue
		}
		state, latest := latestStateInStage(comments, stage)
		if state != BoardRunning {
			continue
		}
		if found == "" || commentNewer(latest, newest) {
			found, newest = stage, latest
		}
	}
	return found
}

// ticketReviewStop reads the operative ticket-review STOP verdict off the card's
// deciding marker. It keys off the SAME `latest` comment DeriveBoardCard already
// resolved, so supersession is free: once a re-dispatch posts a later
// planning-started marker, `latest` is that marker, not the verdict, and this
// returns empty — a re-dispatched card carries no stale stop decision. The head
// set is the authoritative terminalStopVerdicts, never a re-listed literal.
func ticketReviewStop(deciding tracker.Comment) (decision, linked, reasoning string) {
	m, ok := marker.ParseBody(deciding.Body)
	if !ok || m.Type != TicketReviewMarkerType || !terminalStopVerdicts[TicketReviewMarkerType][m.Head] {
		return "", "", ""
	}
	return m.Head, strings.TrimSpace(m.Fields["linked"]), strings.TrimSpace(m.Body)
}

// nothingToDoType is NothingToDoHeader as marker.ParseBody reports it.
const nothingToDoType = "nothing-to-do"

// nothingToDoReason reads why the deciding marker ended the planning stage
// with nothing to plan. Only the operative marker is consulted: a reason from
// a nothing-to-do that a later reopen superseded would label a card that is
// planning again.
func nothingToDoReason(deciding tracker.Comment) string {
	m, ok := marker.ParseBody(deciding.Body)
	if !ok || m.Type != nothingToDoType {
		return ""
	}
	return strings.TrimSpace(m.Fields["reason"])
}

// applyStateOverrides layers the derivation overrides that must run after
// the furthest-stage/latest-marker pass but before the Backlog short-circuit:
// a queued option-decision, a pause on an open question, and a terminal
// determination about the ticket. Split out of DeriveBoardCard so the
// independent `if`s cost this helper's complexity budget rather than the
// parent's (SC-2596 pushed DeriveBoardCard over the gocyclo threshold;
// extracting keeps the override chain readable in one place without
// re-flattening it into the main derivation).
func applyStateOverrides(comments []tracker.Comment, placed Placement, latest tracker.Comment, anyMarker bool) (Placement, tracker.Comment, bool) {
	// A recorded decision ([human:option-chosen]) that no started/terminal marker
	// has yet superseded: the chosen stage is (re)queued while the relaunch's
	// started marker is pending or its launch was deferred to a healthy daemon.
	// Without this the card collapses to the pre-decision running marker and the
	// stuck-running pass falsely reds it (SC-1320). Placed after the SC-910
	// supersede so a decision strictly newer than a stale failure still wins.
	if qStage, qChosen, ok := optionChosenQueued(comments); ok {
		placed, latest, anyMarker = queuedAt(qStage), qChosen, true
	}

	if stagePausedOnOptions(comments, placed.Stage()) {
		placed = placed.pausedOnDecision()
	}

	// A terminal determination is the last word about the whole ticket: the work
	// is already merged, no fix is warranted, a gate stopped the ticket, or the
	// launch was refused for want of a plan. Each files under a stage that ranks
	// BELOW the phantom runs it supersedes — dead launches that died without a
	// terminal marker — so furthest-stage-wins would show a running build nobody
	// is running, forever, and the stuck-running pass rightly spares it as a
	// deliberate stop (SC-3555; the needs-planning case is SC-2596). Surfacing
	// the determination puts the card where a human can act on it: Planning for a
	// refusal, the resolved badge for an already-shipped verdict, Backlog
	// carrying the stop decision for a gate's rejection. Placed after the
	// decision-queue override so a determination strictly newer than a stale
	// option-chosen still wins.
	if terminal, ok := newestTerminalDetermination(comments); ok {
		placed, latest, anyMarker = placed.determinedBy(terminal.placement), terminal.comment, true
	}

	return placed, latest, anyMarker
}

// supersededByNewerMarker reports whether the furthest-stage marker may be
// overridden by a strictly-newer marker anywhere on the ticket. Three cases: a
// stale failure the pipeline has moved past (SC-910); a done-stage PR loop a
// chosen rebuild has restarted from an earlier stage — its strictly-newer
// implementation-started marker retires the loop marker so the card leaves the
// done lane back to Building; and a finished verification a newer rework
// handoff has answered — see the comment on the third disjunct below (SC-4958).
func supersededByNewerMarker(placed Placement, comments []tracker.Comment) bool {
	// An outage marker is transient — a newer *-started marker from the reconcile
	// relaunch retires it, exactly like a stale failure (SC-2307). Without this
	// the card would sit on "machine down" even after the substrate returned and
	// the relaunched agent posted its started marker.
	//
	// A finished review is retired by the handoff that ANSWERS it: a verdict
	// judges the round it read, and a rebuild handed back after it is a built
	// card awaiting review, not a finished review. Without this the card kept
	// describing itself as "reviewed, failed" no matter what happened next — no
	// second review was ever chained, the recovery sweep could not see it, and
	// the board offered only Rework, which started another build against code
	// that was already fixed (SC-4958).
	// A done-stage loop is retired by a rebuild, not by bookkeeping. The arm is
	// written as "any strictly-newer marker", and a handoff re-posted to record
	// the reviewer's own commit is one — so a card whose PR review was live
	// jumped back to implementation/done and a second pre-merge reviewer was
	// launched onto commits the verdict had already judged (SC-5475).
	return placed.State() == BoardFailed || placed.State() == BoardOutage ||
		(placed.Stage() == BoardDoneStage && doneStageLoopActive(comments) && !newestMarkerIsHandoffRepost(comments)) ||
		(placed.Stage() == BoardVerification && placed.State() == BoardDone && handoffAwaitsReview(comments))
}

// currentVerdict is the verdict that still governs the card: empty once a
// handoff has answered it. Read through here rather than off the latest
// review-complete directly, so the deploy gate, the rework affordance and the
// agent-name special case all stop keying on a judgement of a round that is
// over (SC-4958).
func currentVerdict(comments []tracker.Comment) string {
	if handoffAwaitsReview(comments) {
		return ""
	}
	return latestPrefixedLine(comments, ReviewCompleteHeader, "verdict:")
}

// handoffAwaitsReview reports that the ticket's newest [human:ready-for-review]
// is still waiting for the review that judges it: no verification marker at
// all, or a handoff posted after the newest one that hands over work no verdict
// has judged. Recency alone was the whole test until SC-5475 — and under a
// clock a rework round and a bookkeeping re-post are the same comment.
func handoffAwaitsReview(comments []tracker.Comment) bool {
	handoff, ok := latestCommentWithHeader(comments, ReadyForReviewHeader)
	if !ok {
		return false
	}
	judged, ok := latestCommentInStage(comments, BoardVerification)
	if !ok {
		return true
	}
	if !commentNewer(handoff, judged) {
		return false
	}
	return handoffNamesUnjudgedCommit(comments)
}

// handoffNamesUnjudgedCommit reports whether the newest handoff hands over a
// commit the newest verdict did not judge.
//
// True when nothing can be compared — a verdict that records no commits, or a
// handoff that names none — so every thread written before the verdict carried
// its commits keeps the recency answer it has today, and SC-4958's rework case
// (a real rebuild, handed back after a failing verdict) still chains its
// review.
func handoffNamesUnjudgedCommit(comments []tracker.Comment) bool {
	judged := ParseCommitsFromVerdict(latestVerdictBody(comments))
	handed := ParseCommitsFromHandoff(latestHandoffBody(comments))
	if len(judged) == 0 || len(handed) == 0 {
		return true
	}
	for _, sha := range handed {
		if !commitJudged(sha, judged) {
			return true
		}
	}
	return false
}

// handoffIsBookkeepingRepost reports whether the ticket's newest handoff is a
// repost that only records what a verdict has already judged, rather than a
// new round: the handoff must be POSTED AFTER the newest [human:review-complete]
// verdict — not merely name commits that verdict happens to match.
//
// Ordinary rework posts the handoff BEFORE the verdict that judges it (the
// commits matching is the whole point of a passing review), and
// handoffNamesUnjudgedCommit alone cannot tell that case apart from a genuine
// late repost, because both end up with a handoff naming exactly what the
// verdict judged. Recency of handoff-over-verdict is the missing half — a
// repost is written to record a commit a review has ALREADY passed, so it can
// only exist after that verdict; a rework's handoff necessarily precedes the
// verdict it results in. Without this, a fresh rework round handed off and
// re-reviewed (verdict now names exactly the handoff's commits, as every
// current prompt does) was misread as a repost and a stale PR-loop marker
// from a round the ticket had already moved past kept winning the branch
// (SC-5475 follow-up).
func handoffIsBookkeepingRepost(comments []tracker.Comment) bool {
	handoff, ok := latestCommentWithHeader(comments, ReadyForReviewHeader)
	if !ok {
		return false
	}
	verdict, ok := latestCommentWithHeader(comments, ReviewCompleteHeader)
	if !ok || !commentNewer(handoff, verdict) {
		return false
	}
	return !handoffNamesUnjudgedCommit(comments)
}

// commitJudged matches short SHAs against full ones in either direction: the
// handoff writes eight characters and a verdict may quote forty, and reading
// those as different commits would re-open exactly the bug this closes.
func commitJudged(sha string, judged []string) bool {
	sha = strings.ToLower(strings.TrimSpace(sha))
	for _, j := range judged {
		j = strings.ToLower(strings.TrimSpace(j))
		if sha == "" || j == "" {
			continue
		}
		if strings.HasPrefix(sha, j) || strings.HasPrefix(j, sha) {
			return true
		}
	}
	return false
}

// newestMarkerIsHandoffRepost reports that the ticket's newest marker is a
// handoff handing over nothing a verdict has not judged — a record of what the
// branch now holds, not a new round, and so not a marker that retires anything.
func newestMarkerIsHandoffRepost(comments []tracker.Comment) bool {
	newest, ok := latestMarkerOverall(comments)
	if !ok || !strings.HasPrefix(strings.TrimSpace(newest.comment.Body), ReadyForReviewHeader) {
		return false
	}
	return !handoffNamesUnjudgedCommit(comments)
}

// latestVerdictBody returns the body of the newest [human:review-complete], or
// "" when none is present — the mirror of latestHandoffBody, so the two sides
// of the comparison are read the same way.
func latestVerdictBody(comments []tracker.Comment) string {
	latest, ok := latestCommentWithHeader(comments, ReviewCompleteHeader)
	if !ok {
		return ""
	}
	return latest.Body
}

// inlineReviewerOwnsHandoff reports that the newest handoff says its poster
// reviews the work itself AND that reviewer is still plausibly running, so a
// daemon launcher must start no second one.
//
// It is the fact handoffAwaitsReview alone cannot supply. "Nothing has judged
// this round yet" is true for three seconds of every board fix run — between
// the handoff and the container's own [human:review-started] — and in those
// three seconds the daemon's launchers read a finished build with nobody
// reviewing it and start a reviewer that nothing can arbitrate, because the
// in-container reviewer posts no [human:claim] and so is not a participant in
// claimWon (SC-5476, measured on SC-5396: two review-started markers one second
// apart, two verdicts, the later overwriting the earlier).
//
// Liveness is what stops this trading a double review for a stuck card. An
// inline handoff whose implementation container is GONE is an orphan and must
// still be chained — that is SC-430, and suppressing it unconditionally would
// reintroduce it. Where liveness cannot be established at all (no lister wired,
// or the lookup failed) the handoff is taken at its word for StuckRunningGrace
// and no longer: bounded trust, rather than believing it forever or not at all.
// That deviates from this package's usual "a nil lister degrades to today's
// behaviour" convention on purpose — here today's behaviour is the defect.
func inlineReviewerOwnsHandoff(comments []tracker.Comment, pmKey string, alive map[string]struct{}, aliveKnown bool, now time.Time) bool {
	handoff, ok := latestCommentWithHeader(comments, ReadyForReviewHeader)
	if !ok || !HandoffReviewsItself(handoff.Body) {
		return false
	}
	// Once a verification marker newer than the handoff exists, the inline
	// review has recorded itself and the ordinary recency guards govern from
	// there; this predicate has nothing left to protect.
	if !handoffAwaitsReview(comments) {
		return false
	}
	if aliveKnown {
		_, live := liveStageAgent(alive, pmKey, BoardImplementation)
		return live
	}
	return now.Sub(handoff.Created) < StuckRunningGrace
}

// latestCommentWithHeader returns the newest comment whose body starts with
// header, under the board's total order.
func latestCommentWithHeader(comments []tracker.Comment, header string) (tracker.Comment, bool) {
	var latest tracker.Comment
	var have bool
	for _, c := range comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), header) {
			continue
		}
		if !have || commentNewer(c, latest) {
			latest, have = c, true
		}
	}
	return latest, have
}

// latestRunStartInStage returns when the current run occurrence of stage began
// — the Created time of the newest marker in stage whose state is BoardRunning
// ("-started", or a mid-run marker like a passed review that keeps the card
// running). Restarts within the same stage each post their own started marker,
// so the newest one is the boundary for the run that produced the stage's
// CURRENT outcome, not an earlier attempt's. Zero when the stage has no such
// marker (a card with no recorded activity yet).
func latestRunStartInStage(comments []tracker.Comment, stage BoardStage) time.Time {
	var start time.Time
	for _, c := range comments {
		p, ok := fromMarker(c.Body)
		if !ok || p.Stage() != stage || p.State() != BoardRunning {
			continue
		}
		if start.IsZero() || c.Created.After(start) {
			start = c.Created
		}
	}
	return start
}

// latestCommentInStage returns the newest board marker classified into stage.
func latestCommentInStage(comments []tracker.Comment, stage BoardStage) (tracker.Comment, bool) {
	var latest tracker.Comment
	var have bool
	for _, c := range comments {
		p, ok := fromMarker(c.Body)
		if !ok || p.Stage() != stage {
			continue
		}
		if !have || commentNewer(c, latest) {
			latest, have = c, true
		}
	}
	return latest, have
}

// DeployPhasePRReview and DeployPhasePRFix name the two halves of the pre-merge
// review→fix loop, as the board badge reads them. They are separate agents in
// separate containers everywhere else in the machine; the badge used to call
// both of them the review (SC-4151 F15).
const (
	DeployPhasePRReview = "pr-review"
	DeployPhasePRFix    = "pr-fix"
)

// deployPhaseFor names the done-stage sub-phase of a running card: which half of
// the pre-merge review→fix loop is mid-flight, empty for a plain deploy so the
// board badge reads "PR review…" or "fixing PR findings…" rather than
// "deploying…" while the loop runs.
func deployPhaseFor(card BoardCard, comments []tracker.Comment) string {
	if card.Stage != BoardDoneStage {
		return ""
	}
	if card.State == BoardRunning {
		return doneStageLoopHalf(comments)
	}
	// A FAILED done-stage card still has a loop half behind it, and it is the
	// case SC-3852 measured: the loop reds a step whose container goes on
	// working. The badge never reads this — it consults DeployPhase only while
	// running — but AgentNamesForCard does, and without it a red card's agent is
	// never even looked for (SC-4151 A1).
	if card.State == BoardFailed {
		return doneStageStartedHalf(comments)
	}
	return ""
}

// prLoopRoundFor is which review→fix round a running loop card is in, with the
// bound it runs against, or (0, 0) for every card that is not one.
//
// A failed loop card is deliberately excluded even though deployPhaseFor still
// names its half: that half exists for AgentNamesForCard, not for the badge,
// and a red card's question is how far it got (its recorded phase), not which
// round it was on. A count of zero — a thread whose review markers cannot be
// read — is returned as zero rather than as "round 0": an unreadable count must
// degrade to the badge exactly as it rendered before, never to an invented one.
//
// The count is thread-wide rather than scoped to this attempt, because that is
// the number EvaluatePRLoop itself acts on against the same bound: a board that
// disagreed with the loop's own escalation arithmetic would be worse than one
// that agrees with it. That number gives back a round an outage interrupted
// (chargedPRReviewRounds), so the badge counts the rounds that actually ran —
// the same rounds the bound is spent on (SC-5627).
func prLoopRoundFor(card BoardCard, comments []tracker.Comment) (round, bound int) {
	if card.Stage != BoardDoneStage || card.State != BoardRunning || card.DeployPhase == "" {
		return 0, 0
	}
	n := chargedPRReviewRounds(comments)
	if n <= 0 {
		return 0, 0
	}
	return n, DefaultPRReviewRounds
}

// derivePRURL resolves the card's PR link, newest-marker-first: a deployed
// ticket's own pr: line, falling back to the pre-deploy-pipeline pr-pushed
// marker, and finally to a deploy-failed marker's pr: line (the 695
// merge-conflict case, where the PR opened before the deploy step failed) so
// the reconcile pass can confirm-shipped an out-of-band manual merge (SC-910).
func derivePRURL(comments []tracker.Comment) string {
	if url := latestPrefixedLine(comments, DeployedHeader, "pr:"); url != "" {
		return url
	}
	if url := latestPrefixedLine(comments, PRPushedHeader, "pr:"); url != "" {
		return url
	}
	return latestPrefixedLine(comments, DeployFailedHeader, "pr:")
}

// deriveShippedPartial reads the newest [human:shipped-partial] marker off the
// ticket, mirroring how Verdict is read from [human:review-complete]: latest
// wins, so re-planning a deferral supersedes an older trace. ok is false when no
// such marker exists. followOn is the marker's `follow-on` field — the ticket
// that now carries the deferred criteria.
func deriveShippedPartial(comments []tracker.Comment) (followOn string, ok bool) {
	m, found := marker.Latest(comments, ShippedPartialMarkerType)
	if !found {
		return "", false
	}
	return strings.TrimSpace(m.Fields["follow-on"]), true
}

// failureReason extracts the one-line human-readable reason from a *-failed
// marker: the first line of its prose body, skipping the signature fields the
// posting daemon splices in. Falls back to the header for markers posted
// without a reason, so a failed card never shows empty.
//
// If a marker carried blocker fields but no `reason` (both are optional on
// planning-failed/implementation-failed — marker.go's specs), the badge would
// show "kind: <value>" instead of a headline, because blockerLines now
// precedes the prose in failureBody. Every current writer sets reason
// alongside the blocker fields, so this is unreached today; it stays a
// comment rather than a guard because there is no better one-line fallback to
// substitute.
func failureReason(body string) string {
	return firstLine(failureBody(body))
}

// parseResumeLine returns the value of a marker body's "resume:" line (the
// RFC3339 instant pausedOutageBody wrote when a stated recovery time was
// parsed), or "" when the marker carries no such line — the ordinary
// recorded-outage case with no stated time, where the wait falls back to the
// reconcile pass's own backoff.
func parseResumeLine(body string) string {
	return parsePrefixedLine(body, "resume:")
}

// blockerLines renders the blocker fields a *-failed marker may carry, one
// labelled paragraph each in the contract's order, or "" when it carries none.
// Joined with a blank line, not a single "\n": the pane renders this through
// goldmark with no hard-wraps and CSS that keeps white-space normal, so a bare
// "\n" between fields disappears into one run-on paragraph and the labels end
// up buried mid-sentence instead of on their own line (SC-5249).
func blockerLines(fields map[string]string) string {
	var lines []string
	for _, f := range marker.BlockerFields() {
		if v := strings.TrimSpace(fields[f]); v != "" {
			lines = append(lines, f+": "+v)
		}
	}
	return strings.Join(lines, "\n\n")
}

// nonEmptyParts drops the blank sections (a reason-less marker, a marker with
// no blocker fields, an empty prose body) before failureBody joins what is
// left with blank lines — so a section that was never populated does not
// leave a stray gap in the rendered diagnosis.
func nonEmptyParts(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// failureBody returns everything after a *-failed marker's header line — the
// full diagnosis (headline plus markdown detail) for surfaces that can render
// more than one line. Falls back to failureReason so a reason-less marker
// still shows something.
func failureBody(body string) string {
	// Parse rather than cut after the header: every marker is signed before it
	// is posted, and the signature splices `machine:`/`build:` in as the first
	// lines after the header. A positional cut therefore returns the signature
	// and buries the diagnosis, which is what made every failed card read
	// "machine: <id>" instead of saying what went wrong. ParseBody already
	// separates the field block from the prose, so ask it.
	trimmed := strings.TrimSpace(body)
	if m, ok := marker.ParseBody(trimmed); ok {
		// The headline lives in `reason` (the field the *-failed specs require)
		// and the detail in the prose body — failureMarker splits them there, so
		// this is where they are put back together. Either half alone is still a
		// diagnosis: a marker posted before the field existed carries prose only,
		// and a one-line failure carries a reason only.
		// The blocker a needs-human-work stop recorded (kind, evidence,
		// attempted, release) sits between the two: it is why the evidence was
		// put on the marker at all — so the person on the red card is not sent
		// to the tracker comment to learn what the machine already found
		// (SC-5249).
		parts := nonEmptyParts(strings.TrimSpace(m.Fields["reason"]), blockerLines(m.Fields), strings.TrimSpace(m.Body))
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n")
		}
		// A marker carrying neither has no diagnosis to give: show the header.
		return firstLine(trimmed)
	}
	// Not a marker at all: there is no field block to skip and no prose to find,
	// so the first line is all there is.
	return firstLine(trimmed)
}

// latestStateInStage resolves the stage's state from its newest marker and
// returns that marker's comment so a failure message can be extracted.
func latestStateInStage(comments []tracker.Comment, stage BoardStage) (BoardState, tracker.Comment) {
	latest, ok := latestCommentInStage(comments, stage)
	if !ok {
		return BoardIdle, tracker.Comment{}
	}
	p, _ := fromMarker(latest.Body)
	return p.State(), latest
}

// latestMarkerOverall returns the newest board marker across ALL stages — the
// placement it classifies to and the comment carrying it — and whether any
// marker exists. Recency is global (by Created), so a re-implementation
// restarted in an earlier stage or a later deploy is seen even when the
// furthest stage's own newest marker is a stale failure (SC-910).
func latestMarkerOverall(comments []tracker.Comment) (markerPlacement, bool) {
	var latest markerPlacement
	var have bool
	for _, c := range comments {
		p, ok := fromMarker(c.Body)
		if !ok {
			continue
		}
		if !have || commentNewer(c, latest.comment) {
			latest, have = markerPlacement{placement: p, comment: c}, true
		}
	}
	return latest, have
}

// hasPlanEvidence reports whether the ticket has been planned. Two proofs, one
// per topology: a [human:plan] comment (the plan itself lives on the ticket,
// single-tracker topology) or a [human:plan-ready] marker (planning completed;
// both topologies post it, carrying the engineering key in split topology).
// Either is sufficient that the implementation stage has a plan to carry out —
// the precondition the launch guard checks (SC-2596).
func hasPlanEvidence(comments []tracker.Comment) bool {
	if _, ok := latestPlanComment(comments); ok {
		return true
	}
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), PlanReadyHeader) {
			return true
		}
	}
	return false
}

// newestTerminalDetermination returns the ticket's newest board marker when it
// is a registered terminal resolution (isTerminalResolution), together with the
// stage and state that marker classifies to. Newest-OVERALL rather than
// furthest-stage on purpose: a determination about the whole ticket must win
// over the phantom runs it supersedes, which outrank the stage it files under.
// Newest-ONLY on purpose too: a genuine re-dispatch posts a later *-started
// marker, and the card must then follow the new run rather than stay parked on a
// retired verdict (SC-3555). Returning the marker's own comment makes it the
// card's deciding `latest`, so the reason, the posting daemon, the stage-entered
// time and a gate's StopDecision/StopReasoning all come off the determination
// instead of the phantom.
func newestTerminalDetermination(comments []tracker.Comment) (markerPlacement, bool) {
	newest, ok := latestMarkerOverall(comments)
	if !ok || !isTerminalResolution(newest.comment.Body) {
		return markerPlacement{}, false
	}
	return newest, true
}

// latestPlanComment returns the body of the newest [human:plan] comment with
// the header line stripped. The latest wins so re-planning supersedes older
// plans without editing comment history.
func latestPlanComment(comments []tracker.Comment) (string, bool) {
	var body string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		if !strings.HasPrefix(trimmed, PlanCommentHeader) {
			continue
		}
		if !haveLatest || commentNewer(c, latest) {
			latest = c
			haveLatest = true
			// ParseBody, not TrimPrefix: a signed plan comment carries machine:/build:
			// between the header and the plan, and trimming only the header prefixed
			// every rendered plan with the signature.
			body = marker.Prose(trimmed)
		}
	}
	return body, haveLatest
}

// firstEngineeringKey resolves the engineering ticket key from the comment
// thread. Both [human:plan-ready] and [human:ready-for-review] carry an
// `engineering:` line, but ParseEngineeringKeysFromHandoff only matches the
// latter header — so scan plan-ready bodies directly as a fallback. The
// latest-by-time marker wins.
func firstEngineeringKey(comments []tracker.Comment) string {
	var key string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		var k string
		if keys := ParseEngineeringKeysFromHandoff(c.Body); len(keys) > 0 {
			k = keys[0]
		} else if strings.HasPrefix(strings.TrimSpace(c.Body), PlanReadyHeader) {
			k = parsePrefixedLine(c.Body, "engineering:")
		}
		if k == "" {
			continue
		}
		if !haveLatest || commentNewer(c, latest) {
			latest = c
			haveLatest = true
			key = k
		}
	}
	return key
}

// latestPrefixedLine returns the value of the given prefixed line from the
// latest comment whose body starts with header. Used for branch: (on
// ready-for-review) and pr: (on pr-pushed).
func latestPrefixedLine(comments []tracker.Comment, header, prefix string) string {
	latest, ok := latestCommentWithHeader(comments, header)
	if !ok {
		return ""
	}
	return parsePrefixedLine(latest.Body, prefix)
}

// latestHandoffBody returns the full body of the latest [human:ready-for-review]
// handoff comment, or "" when none is present. Callers parse it for the branch
// and commit SHAs a review or deploy binds against — reading the whole body once
// rather than re-scanning per field.
func latestHandoffBody(comments []tracker.Comment) string {
	latest, ok := latestCommentWithHeader(comments, ReadyForReviewHeader)
	if !ok {
		return ""
	}
	return latest.Body
}

// parsePrefixedLine returns the trimmed value following the first line that
// begins with prefix (e.g. "engineering:"), or "" when absent.
func parsePrefixedLine(body, prefix string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// firstLine returns the first non-empty line of a body, used as the error
// summary for a failed marker.
func firstLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
