package daemon

import (
	"context"
	"strconv"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// OptionsHeader marks a machine-readable decision block: a review (or any
// stage) that ends in a fork posts the choices as one line each, with the
// full reasoning staying in the stage's own comment. Deliberately absent
// from orderedMarkerSpecs — options describe a pending human decision, not
// a stage/state transition.
const OptionsHeader = "[human:options]"

// OptionChosenHeader records the pick: audit trail on the ticket and the
// consumption signal that removes the block from the card.
const OptionChosenHeader = "[human:option-chosen]"

// BoardOption is one selectable direction from an options block.
type BoardOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// optionStages are the stages an options block may relaunch — exactly the
// agent-launching stages. A block naming anything else is not silently dropped
// (see parseOptionsBlock / attachOpenOptions): it surfaces as a visible card
// error so a typo, or a new pipeline stage that forgets to register here, cannot
// vanish (SC-2137).
var optionStages = map[BoardStage]bool{
	BoardPlanning:       true,
	BoardImplementation: true,
	BoardVerification:   true,
}

// optionStageAliases resolves a decision block's stage: field to the board stage
// that owns it, for gate phases that run INSIDE another stage's dispatch rather
// than as a column of their own. The ticket-review gate runs as the first phase
// of the planning stage (planPrompt), so a decision it raises ("stage:
// ticket-review") resumes planning — which re-runs the gate — and renders in the
// planning column it already occupies while the gate runs. Without this the
// gate's decision named a stage no whitelist listed and was parsed to nothing,
// so it never reached the board (SC-2137).
var optionStageAliases = map[BoardStage]BoardStage{
	BoardTicketReview: BoardPlanning,
}

// parseOptionsBlock extracts (stage, context, options) from an options
// comment. The grammar is line-based like every other marker:
//
//	[human:options]
//	stage: implementation
//	context: review found a blocking design gap
//	1: <option label>
//	2: <option label>
//
// The returned stage is CANONICAL: a gate alias (optionStageAliases, e.g.
// ticket-review → planning) is resolved here so every caller sees the board
// stage the decision resumes. A block with no stage line or no options is
// malformed and returns empty; a well-formed block naming a stage the board
// cannot resume is NOT dropped — it is returned as-is so callers surface it as a
// visible error rather than letting it vanish (SC-2137). Resumability is a
// separate check (optionStages[stage]), never conflated with "well-formed".
func parseOptionsBlock(body string) (BoardStage, string, []BoardOption) {
	var raw BoardStage
	var context string
	var opts []BoardOption
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			// The [human:options] header itself (and any stray marker line).
		case strings.HasPrefix(line, "stage:"):
			raw = BoardStage(strings.TrimSpace(strings.TrimPrefix(line, "stage:")))
		case strings.HasPrefix(line, "context:"):
			context = strings.TrimSpace(strings.TrimPrefix(line, "context:"))
		case strings.HasPrefix(line, DaemonLinePrefix):
			// Provenance, not a choice. Every marker body carries this stamp, and
			// `daemon: <id>` matches the id:label shape below exactly — so without
			// this case the board offered the daemon id as a selectable answer,
			// and counted it toward the number of answers a block appears to have.
		default:
			id, label, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(id) == "" || strings.ContainsAny(id, " \t") {
				continue
			}
			if label = strings.TrimSpace(label); label != "" {
				opts = append(opts, BoardOption{ID: strings.TrimSpace(id), Label: label})
			}
		}
	}
	// A block with no stage line or no options is not a decision — ignore it. An
	// unrecognized-but-well-formed stage is deliberately kept (see doc): the
	// silent drop is exactly the defect this ticket removes.
	if raw == "" || len(opts) == 0 {
		return "", "", nil
	}
	stage := raw
	if canon, ok := optionStageAliases[raw]; ok {
		stage = canon
	}
	return stage, context, opts
}

// openOptionsBlock returns the latest options block that is still awaiting a
// decision. Consumption: any LATER option-chosen comment or stage-started
// marker closes it — a pursued (or superseded) decision must stop asking.
func openOptionsBlock(comments []tracker.Comment) (tracker.Comment, bool) {
	var latest tracker.Comment
	var found bool
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), OptionsHeader) &&
			(!found || commentNewer(c, latest)) {
			latest = c
			found = true
		}
	}
	if !found {
		return tracker.Comment{}, false
	}
	for _, c := range comments {
		if !commentNewer(c, latest) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(c.Body), OptionChosenHeader) {
			return tracker.Comment{}, false
		}
		if _, state, ok := ClassifyMarker(c.Body); ok && state == BoardRunning {
			return tracker.Comment{}, false
		}
	}
	return latest, true
}

// optionChosenQueued reports the stage a recorded decision has (re)queued, when
// the newest board-relevant signal is an [human:option-chosen] comment with no
// later started/terminal marker. It returns the chosen stage (parsed from the
// [human:options] block the decision consumed) and the choice comment, so
// DeriveBoardCard can synthesize a BoardQueued state for the window before the
// relaunch's started marker lands — or, when the launch is deferred to a healthy
// daemon, indefinitely (SC-1320). Any classified marker strictly newer than the
// choice supersedes it (latest-wins).
func optionChosenQueued(comments []tracker.Comment) (BoardStage, tracker.Comment, bool) {
	chosen, ok := latestOptionChosen(comments)
	if !ok {
		return "", tracker.Comment{}, false
	}
	if hasLaterMarker(comments, chosen) {
		return "", tracker.Comment{}, false
	}
	block, ok := latestOptionsBlockAtOrBefore(comments, chosen)
	if !ok {
		return "", tracker.Comment{}, false
	}
	stage, _, opts := parseOptionsBlock(block.Body)
	// Only a resumable stage synthesizes a queued placement — an unrecognized
	// stage cannot be relaunched, so there is no pending relaunch to represent
	// (it surfaces as a card error instead, SC-2137).
	if len(opts) == 0 || !optionStages[stage] {
		return "", tracker.Comment{}, false
	}
	return stage, chosen, true
}

// latestOptionChosen returns the newest [human:option-chosen] comment.
func latestOptionChosen(comments []tracker.Comment) (tracker.Comment, bool) {
	var chosen tracker.Comment
	var have bool
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), OptionChosenHeader) &&
			(!have || commentNewer(c, chosen)) {
			chosen, have = c, true
		}
	}
	return chosen, have
}

// hasLaterMarker reports whether any classified board marker lands strictly
// after the decision comment — used to detect a started/terminal marker that
// supersedes a recorded decision (latest-wins).
func hasLaterMarker(comments []tracker.Comment, since tracker.Comment) bool {
	for _, c := range comments {
		if !commentNewer(c, since) {
			continue
		}
		if _, _, ok := ClassifyMarker(c.Body); ok {
			return true
		}
	}
	return false
}

// latestOptionsBlockAtOrBefore returns the newest [human:options] block posted
// at or before the decision comment — the block a decision at that time would
// have consumed.
func latestOptionsBlockAtOrBefore(comments []tracker.Comment, until tracker.Comment) (tracker.Comment, bool) {
	var block tracker.Comment
	var have bool
	for _, c := range comments {
		if commentNewer(c, until) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(c.Body), OptionsHeader) &&
			(!have || commentNewer(c, block)) {
			block, have = c, true
		}
	}
	return block, have
}

// BoardOptionRequest is the wire request for choosing one option from a
// card's open decision block.
type BoardOptionRequest struct {
	PMKey    string `json:"pm_key"`
	OptionID string `json:"option_id"`
}

// ApplyOption records a human's choice from the ticket's open options block
// and relaunches the block's stage with the choice injected into the prompt —
// the same shape as the rework loop, but directed. The click is the consent;
// the option-chosen comment is the audit trail AND the consumption signal, so
// a stale UI or double-click finds no open block and dispatches nothing. The
// original agent's container is long gone by decision time: a fresh run with
// the ticket as its memory is the only correct continuation.
func (d BoardTransitionDeps) ApplyOption(ctx context.Context, req BoardOptionRequest) error {
	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM comments for option", "pm", req.PMKey)
	}
	block, ok := openOptionsBlock(comments)
	if !ok {
		return errors.WithDetails("no open decision on this ticket — the options were already pursued or superseded", "pm", req.PMKey)
	}
	stage, _, opts := parseOptionsBlock(block.Body)
	// A well-formed block naming a stage the board cannot resume must not be
	// silently launched into the wrong stage (startedHeaderFor/stagePrompt would
	// otherwise fall through to their implementation defaults). The card already
	// shows this as an error (attachOpenOptions); refuse the choice loudly too.
	if !optionStages[stage] {
		return errors.WithDetails("decision names a stage the board cannot resume", "pm", req.PMKey, "stage", string(stage))
	}
	chosen, ok := findOption(opts, req.OptionID)
	if !ok {
		return errors.WithDetails("unknown option id", "pm", req.PMKey, "option", req.OptionID)
	}

	if _, err := d.Commenter.AddComment(ctx, req.PMKey,
		OptionChosenHeader+" "+chosen.ID+": "+chosen.Label); err != nil {
		return errors.WrapWithDetails(err, "recording option choice", "pm", req.PMKey)
	}

	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	prompt := stagePrompt(stage, req.PMKey, card) +
		" — a decision was made on this ticket: pursue the direction in the latest " +
		OptionChosenHeader + " comment (" + chosen.Label + ")"
	// A decision click is human-initiated, like the Fix/Retry entry points: the
	// interval since the decision became available is the human's think-time,
	// not a pipeline wait, so it is suppressed (empty cause, SC-2462). Resuming an
	// implementation-stage decision still executes a plan, so it is plan-gated
	// like any other implementation launch (SC-2596).
	return d.startAgentStage(ctx, req.PMKey, stage, startedHeaderFor(stage), prompt, WaitCause(""), stage == BoardImplementation)
}

func findOption(opts []BoardOption, id string) (BoardOption, bool) {
	for _, o := range opts {
		if o.ID == id {
			return o, true
		}
	}
	return BoardOption{}, false
}

// stagePrompt is the stage's normal dispatch prompt — an option relaunch runs
// the same skill the stage always runs, plus the direction suffix.
//
// Planning uses planPrompt, not a bare /human-plan, so a resumed planning
// decision re-runs the ticket-review gate exactly like every other planning
// launch (the forward move and the in-place retry both use planPrompt). This is
// what lets a ticket-review decision — aliased to planning — resume the gate: no
// [human:ticket-review] verdict exists yet, so planPrompt re-runs the gate with
// the choice injected; a plain planning decision already carries a verdict, so
// planPrompt skips the gate and goes straight to planning (SC-2137).
func stagePrompt(stage BoardStage, pmKey string, card BoardCard) string {
	switch stage {
	case BoardPlanning:
		return planPrompt(pmKey)
	case BoardVerification:
		return "/human-review " + dispatchKey(pmKey, card)
	default:
		return "/human-execute " + dispatchKey(pmKey, card)
	}
}

// startedHeaderFor maps an agent-launching stage to its started marker.
func startedHeaderFor(stage BoardStage) string {
	switch stage {
	case BoardPlanning:
		return PlanningStartedHeader
	case BoardVerification:
		return ReviewStartedHeader
	default:
		return ImplementationStartedHeader
	}
}

// attachOpenOptions decorates a derived card with the latest unconsumed
// options block, if any. A well-formed block naming a stage the board cannot
// resume is surfaced as a visible card error instead of being attached as a
// clickable decision: dropping it is how the ticket-review gate's decision
// stayed invisible, so an unregistered stage now reds the card loudly rather
// than vanishing (SC-2137).
func attachOpenOptions(card *BoardCard, comments []tracker.Comment) {
	block, ok := openOptionsBlock(comments)
	if !ok {
		return
	}
	stage, context, opts := parseOptionsBlock(block.Body)
	if len(opts) == 0 {
		return
	}
	// A block offering fewer answers than a choice needs is malformed in the same
	// way as one naming an unresumable stage, and gets the same treatment: say
	// what is wrong on the card rather than presenting a dead end as a decision.
	// Posting is now rejected for this (marker.MinDecisionOptions), so this is
	// the recovery path for blocks already on a ticket.
	if len(opts) < marker.MinDecisionOptions {
		card.State = BoardFailed
		card.Error = "decision block offers only " + strconv.Itoa(len(opts)) + " answer; a decision needs at least " +
			strconv.Itoa(marker.MinDecisionOptions) + " — the stage should have handled this itself"
		return
	}
	if !optionStages[stage] {
		card.State = BoardFailed
		card.Error = "decision block names stage \"" + string(stage) + "\" the board cannot resume"
		return
	}
	card.Options = opts
	card.OptionsContext = context
	card.OptionsStage = stage
}
