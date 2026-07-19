package daemon

import (
	"context"
	"strings"

	"github.com/gethuman-sh/human/errors"
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
// agent-launching stages. A block naming anything else is ignored so a typo
// can never dispatch the done stage or nothing at all.
var optionStages = map[BoardStage]bool{
	BoardPlanning:       true,
	BoardImplementation: true,
	BoardVerification:   true,
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
// A missing or non-agent stage invalidates the whole block (empty return).
func parseOptionsBlock(body string) (BoardStage, string, []BoardOption) {
	var stage BoardStage
	var context string
	var opts []BoardOption
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			// The [human:options] header itself (and any stray marker line).
		case strings.HasPrefix(line, "stage:"):
			stage = BoardStage(strings.TrimSpace(strings.TrimPrefix(line, "stage:")))
		case strings.HasPrefix(line, "context:"):
			context = strings.TrimSpace(strings.TrimPrefix(line, "context:"))
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
	if !optionStages[stage] || len(opts) == 0 {
		return "", "", nil
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
			(!found || c.Created.After(latest.Created)) {
			latest = c
			found = true
		}
	}
	if !found {
		return tracker.Comment{}, false
	}
	for _, c := range comments {
		if !c.Created.After(latest.Created) {
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
	return d.startAgentStage(ctx, req.PMKey, stage, startedHeaderFor(stage), prompt)
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
func stagePrompt(stage BoardStage, pmKey string, card BoardCard) string {
	switch stage {
	case BoardPlanning:
		return "/human-plan " + pmKey
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
// options block, if any.
func attachOpenOptions(card *BoardCard, comments []tracker.Comment) {
	block, ok := openOptionsBlock(comments)
	if !ok {
		return
	}
	stage, context, opts := parseOptionsBlock(block.Body)
	if len(opts) == 0 {
		return
	}
	card.Options = opts
	card.OptionsContext = context
	card.OptionsStage = stage
}
