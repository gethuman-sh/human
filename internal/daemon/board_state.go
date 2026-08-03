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
	// HasPlan reports a [human:plan] comment on the ticket — the plan lives
	// here instead of on a separate engineering ticket (single-tracker
	// topology).
	HasPlan bool `json:"has_plan,omitempty"`
	// HasRelatedRecord reports a COMPLETED filing-time related-work record
	// ([human:related] found/none) on the ticket. The frontend uses it to
	// suppress the on-demand "Find related work" card menu item — an incomplete
	// record does not set it, so a died-halfway run stays re-runnable (SC-2405).
	HasRelatedRecord bool `json:"has_related_record,omitempty"`
	// Verdict is the `verdict:` line of the latest [human:review-complete]
	// comment (pass / pass with notes / fail / incomplete). A fail or incomplete
	// verdict keeps the card out of Ready to Deploy and blocks the deploy
	// transition; an absent verdict counts as pass so threads reviewed before
	// verdicts existed keep flowing.
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
	// StageEnteredAt is the Created time of the newest marker in the card's
	// current stage — for a plan-done card, when the current plan landed. The
	// board renders it as an age badge so work rotting in a queue is visible.
	StageEnteredAt time.Time `json:"stage_entered_at,omitzero"`
	// StageDaemonID is the posting daemon stamped on that same deciding marker
	// (StampDaemon / ParseDaemonID). It tells the durable stuck-running reconcile
	// pass which daemon owns a running stage, so a peer daemon spares a live
	// foreign-owned card instead of reddening work it simply cannot see locally
	// (SC-1450). Empty for an unstamped marker, preserving single-daemon
	// behaviour.
	StageDaemonID string `json:"stage_daemon_id,omitempty"`
	// DeployPhase names the done-stage sub-phase for a running card: "pr-review"
	// while the machine review→fix loop is mid-flight, empty for a plain deploy.
	// It lets the board badge read "PR review…" instead of "deploying…" so the
	// loop is visible while it runs.
	DeployPhase string `json:"deploy_phase,omitempty"`
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
		return BoardCard{Stage: BoardHidden}
	}

	if isIdea {
		return BoardCard{Stage: BoardIdeas}
	}

	furthest := BoardBacklog
	furthestRank := -1
	var anyMarker bool

	// First pass: find the furthest stage that any marker reaches.
	for _, c := range comments {
		stage, _, ok := ClassifyMarker(c.Body)
		if !ok {
			continue
		}
		anyMarker = true
		if r := stageRank[stage]; r > furthestRank {
			furthestRank = r
			furthest = stage
		}
	}

	_, hasPlan := latestPlanComment(comments)
	hasRelated := hasCompletedRelatedRecord(comments)

	var state BoardState
	var latest tracker.Comment
	if anyMarker {
		// Second pass: within the furthest stage, the latest marker decides state.
		state, latest = latestStateInStage(comments, furthest)

		// A furthest-stage failure is authoritative only while it is the ticket's
		// newest marker. A strictly-newer marker anywhere — a re-implementation
		// restarting from an earlier stage (ticket 881) or a later deploy — retires
		// the stale red; the card follows the ticket's current activity rather than a
		// terminal failure the pipeline already moved past (SC-910).
		if supersededByNewerMarker(state, furthest, comments) {
			if newest, newestStage, newestState, ok := latestMarkerOverall(comments); ok && commentNewer(newest, latest) {
				furthest, state, latest = newestStage, newestState, newest
			}
		}
	}

	furthest, state, latest, anyMarker = applyStateOverrides(comments, furthest, state, latest, anyMarker)

	if !anyMarker {
		// No pipeline activity yet: the open ticket waits in Backlog.
		return BoardCard{Stage: BoardBacklog, HasPlan: hasPlan, HasRelatedRecord: hasRelated}
	}

	card := BoardCard{Stage: furthest, State: state, HasPlan: hasPlan, HasRelatedRecord: hasRelated, StageEnteredAt: latest.Created, StageDaemonID: ParseDaemonID(latest.Body)}
	card.EngineeringKey = firstEngineeringKey(comments)
	card.Branch = latestPrefixedLine(comments, ReadyForReviewHeader, "branch:")
	card.Commits = latestPrefixedLine(comments, ReadyForReviewHeader, "commits:")
	card.Verdict = latestPrefixedLine(comments, ReviewCompleteHeader, "verdict:")
	card.PRURL = derivePRURL(comments)
	if followOn, ok := deriveShippedPartial(comments); ok {
		card.ShippedPartial = true
		card.ShippedPartialFollowOn = followOn
	}
	// An outage card carries the same one-line reason a failed card does (the
	// substrate that was unreachable), so the badge can say WHAT is down, not just
	// that it is — the outage marker's body is composed exactly like a failure's
	// (SC-2307).
	if state == BoardFailed || state == BoardOutage {
		card.Error = failureReason(latest.Body)
	}
	card.DeployPhase = deployPhaseFor(card, comments)
	card.StopDecision, card.StopLinkedKey, card.StopReasoning = ticketReviewStop(latest)
	attachOpenOptions(&card, comments)
	return card
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

// applyStateOverrides layers the two derivation overrides that must run after
// the furthest-stage/latest-marker pass but before the Backlog short-circuit:
// a queued option-decision and a needs-planning refusal. Split out of
// DeriveBoardCard so the two independent `if`s cost this helper's complexity
// budget rather than the parent's (SC-2596 pushed DeriveBoardCard over the
// gocyclo threshold; extracting keeps the override chain readable in one place
// without re-flattening it into the main derivation).
func applyStateOverrides(comments []tracker.Comment, furthest BoardStage, state BoardState, latest tracker.Comment, anyMarker bool) (BoardStage, BoardState, tracker.Comment, bool) {
	// A recorded decision ([human:option-chosen]) that no started/terminal marker
	// has yet superseded: the chosen stage is (re)queued while the relaunch's
	// started marker is pending or its launch was deferred to a healthy daemon.
	// Without this the card collapses to the pre-decision running marker and the
	// stuck-running pass falsely reds it (SC-1320). Placed after the SC-910
	// supersede so a decision strictly newer than a stale failure still wins.
	if qStage, qChosen, ok := optionChosenQueued(comments); ok {
		furthest, state, latest, anyMarker = qStage, BoardQueued, qChosen, true
	}

	state = pauseOnOpenOptions(state, furthest, comments)

	// A [human:needs-planning] refusal is the last word: the implementation
	// launch was refused because the ticket has no plan (SC-2596). It is a
	// planning-stage marker, but the ticket's furthest markers are the phantom
	// implementation launches it refused, which furthest-stage-wins would show
	// as a running/failed build instead. Surface the refusal explicitly so the
	// card returns to Planning carrying the determination — where a human can
	// trigger the plan (isPlanningRetry) — and no phantom running marker reds it
	// as a crash. Placed after the decision-queue override so a refusal strictly
	// newer than a stale option-chosen still wins.
	if np, ok := newestNeedsPlanning(comments); ok {
		furthest, state, latest, anyMarker = BoardPlanning, BoardFailed, np, true
	}

	return furthest, state, latest, anyMarker
}

// pauseOnOpenOptions turns a running OR failed state into a waiting-on-human
// state when an open [human:options] block names the card's own stage or an
// earlier stage the answer would rework: the card is not working, and it is
// not dead either — it is waiting for a human. Server-side twin of the
// client's decision-badge branch. Uses the same at-or-before stage-rank
// predicate as the failure watcher and reconcile pass (stagePausedOnOptions),
// so a block naming a stage the card has not yet reached — a stale or
// target-relaunch block — never clears an active run (SC-1669).
//
// Accepting BoardFailed alongside BoardRunning is the residual safety net
// (SC-1957): a card can reach this point already reddened by a *-failed
// marker posted before the recovery machinery's own relaunch consumed the
// question (openOptionsBlock only treats a later BoardRunning marker or an
// option-chosen as consumption — a *-failed marker does not). Surfacing that
// combination as waiting-on-human rather than a plain failure is exactly what
// makes an otherwise-erased question visible. This also subsumes the former
// SC-1857 done-stage PR-loop escalation special case: the escalation's block
// names the implementation stage while the card's furthest stage is done, so
// the general at-or-before rank rule (rank 3 <= rank 5) pauses it the same way
// the former bespoke escalation check used to.
func pauseOnOpenOptions(state BoardState, furthest BoardStage, comments []tracker.Comment) BoardState {
	if (state == BoardRunning || state == BoardFailed) && stagePausedOnOptions(comments, furthest) {
		return BoardIdle
	}
	return state
}

// supersededByNewerMarker reports whether the furthest-stage marker may be
// overridden by a strictly-newer marker anywhere on the ticket. Two cases: a
// stale failure the pipeline has moved past (SC-910), and a done-stage PR loop a
// chosen rebuild has restarted from an earlier stage — its strictly-newer
// implementation-started marker retires the loop marker so the card leaves the
// done lane back to Building.
func supersededByNewerMarker(state BoardState, furthest BoardStage, comments []tracker.Comment) bool {
	// An outage marker is transient — a newer *-started marker from the reconcile
	// relaunch retires it, exactly like a stale failure (SC-2307). Without this
	// the card would sit on "machine down" even after the substrate returned and
	// the relaunched agent posted its started marker.
	return state == BoardFailed || state == BoardOutage ||
		(furthest == BoardDoneStage && doneStageLoopActive(comments))
}

// deployPhaseFor names the done-stage sub-phase of a running card: "pr-review"
// while the pre-merge review→fix loop is mid-flight, empty for a plain deploy so
// the board badge reads "PR review…" rather than "deploying…" while the loop
// runs.
func deployPhaseFor(card BoardCard, comments []tracker.Comment) string {
	if card.Stage == BoardDoneStage && card.State == BoardRunning && doneStageLoopActive(comments) {
		return "pr-review"
	}
	return ""
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

// failureReason extracts the human-readable reason from a *-failed marker: the
// first non-empty line after the header. Falls back to the header itself for
// markers posted without a reason, so a failed card never shows empty.
func failureReason(body string) string {
	trimmed := strings.TrimSpace(body)
	if _, after, ok := strings.Cut(trimmed, "\n"); ok {
		if reason := firstLine(after); reason != "" {
			return reason
		}
	}
	return firstLine(trimmed)
}

// failureBody returns everything after a *-failed marker's header line — the
// full diagnosis (headline plus markdown detail) for surfaces that can render
// more than one line. Falls back to failureReason so a reason-less marker
// still shows something.
func failureBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if _, after, ok := strings.Cut(trimmed, "\n"); ok {
		if rest := strings.TrimSpace(after); rest != "" {
			return rest
		}
	}
	return failureReason(body)
}

// latestStateInStage resolves the stage's state from its newest marker and
// returns that marker's comment so a failure message can be extracted.
func latestStateInStage(comments []tracker.Comment, stage BoardStage) (BoardState, tracker.Comment) {
	state := BoardIdle
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		s, st, ok := ClassifyMarker(c.Body)
		if !ok || s != stage {
			continue
		}
		if !haveLatest || commentNewer(c, latest) {
			latest = c
			haveLatest = true
			state = st
		}
	}
	return state, latest
}

// latestMarkerOverall returns the newest board marker across ALL stages — its
// comment, stage, and state — and whether any marker exists. Recency is global
// (by Created), so a re-implementation restarted in an earlier stage or a later
// deploy is seen even when the furthest stage's own newest marker is a stale
// failure (SC-910).
func latestMarkerOverall(comments []tracker.Comment) (tracker.Comment, BoardStage, BoardState, bool) {
	var latest tracker.Comment
	var stage BoardStage
	var state BoardState
	var have bool
	for _, c := range comments {
		st, s, ok := ClassifyMarker(c.Body)
		if !ok {
			continue
		}
		if !have || commentNewer(c, latest) {
			latest, stage, state, have = c, st, s, true
		}
	}
	return latest, stage, state, have
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

// newestNeedsPlanning reports whether the ticket's newest board marker is a
// [human:needs-planning] refusal, returning that marker so its message reaches
// the card. Newest-overall (not furthest-stage) on purpose: the refusal must win
// over the phantom implementation markers it refused, which outrank the planning
// stage it maps to (SC-2596).
func newestNeedsPlanning(comments []tracker.Comment) (tracker.Comment, bool) {
	newest, _, _, ok := latestMarkerOverall(comments)
	if !ok {
		return tracker.Comment{}, false
	}
	if strings.HasPrefix(strings.TrimSpace(newest.Body), NeedsPlanningHeader) {
		return newest, true
	}
	return tracker.Comment{}, false
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
			body = strings.TrimSpace(strings.TrimPrefix(trimmed, PlanCommentHeader))
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
	var value string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), header) {
			continue
		}
		if !haveLatest || commentNewer(c, latest) {
			latest = c
			haveLatest = true
			value = parsePrefixedLine(c.Body, prefix)
		}
	}
	return value
}

// latestHandoffBody returns the full body of the latest [human:ready-for-review]
// handoff comment, or "" when none is present. Callers parse it for the branch
// and commit SHAs a review or deploy binds against — reading the whole body once
// rather than re-scanning per field.
func latestHandoffBody(comments []tracker.Comment) string {
	var body string
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), ReadyForReviewHeader) {
			continue
		}
		if !haveLatest || commentNewer(c, latest) {
			latest = c
			haveLatest = true
			body = c.Body
		}
	}
	return body
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
