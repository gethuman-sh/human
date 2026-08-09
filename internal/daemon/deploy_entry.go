package daemon

import (
	"context"
	stderrors "errors"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// ErrDeployAwaitingDecision is the deploy's one NON-FAILURE refusal: the ticket
// is paused on an open [human:options] block, which is the single state nothing
// downstream may move. It is a sentinel rather than a message so a caller can
// tell it apart from a deploy that actually broke — a refusal must never be
// reported as a crash, must never post [human:deploy-failed], and must never
// red the card.
var ErrDeployAwaitingDecision = stderrors.New("the ticket is waiting on a decision")

// StartDeployRequest is what a deploy needs to know before it runs.
type StartDeployRequest struct {
	PMKey  string
	Title  string
	PRBody string
	Branch string
	// OverrideDecision ships even while an open decision waits. Deliberate and
	// explicit: the guard exists because a person was asked a question, so only a
	// person may decide to ship past it.
	OverrideDecision bool
}

// StartDeploy is the deploy stage's single entry point for any route that STARTS
// a deploy from outside the board's own transition path — today `human deploy`.
// It owes the ticket two things the engine cannot know about: the refusal while a
// decision is open, and the record that the work began. Both used to live in the
// caller, which is why the CLI route had neither (SC-3852).
//
// The board's three in-flight calls to DeployBranch are deliberately NOT routed
// here: they are continuations of a deploy the Done stage already recorded as
// [human:pr-review-started], not starts. A new route that begins a deploy belongs
// here, not on the engine.
func (d BoardTransitionDeps) StartDeploy(ctx context.Context, req StartDeployRequest) error {
	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM comments before the deploy", "pm", req.PMKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	override := ""
	if awaitingDecision(card) {
		if !req.OverrideDecision {
			return errors.WrapWithDetails(ErrDeployAwaitingDecision,
				"deploy refused: this ticket is waiting on a decision — answer the open [human:options] block, or re-run with --override-decision",
				"pm", req.PMKey, "stage", string(card.Stage))
		}
		// openOptionsBlock (board_options.go:127-137) retires the open block on
		// ANY later BoardRunning-classified marker, and deploy-started is one — so
		// an override that leaves no trace would make the board silently forget
		// the question was ever asked. Name what was walked past instead.
		override = "\noverride: deployed with an open decision on stage " + string(card.OptionsStage) +
			" — " + card.OptionsContext
	}
	// Best-effort, exactly like the PR loop's converging marker: the merge is the
	// work, and refusing to ship over a lost sentence trades code for a comment.
	if _, err := d.Commenter.AddComment(ctx, req.PMKey, deployStartedBody(req.Branch, override)); err != nil {
		d.Logger.Warn().Err(err).Str("pm", req.PMKey).
			Msg("deploy: could not record the start on the ticket; continuing to the gate")
	}
	return d.DeployBranch(ctx, req.PMKey, req.Title, req.PRBody, req.Branch)
}

// deployStartedBody names the branch on the start marker so a reader can tell
// WHICH branch a deploy carried without waiting for the merge to say so, and
// carries the override line (empty unless the deploy walked past an open
// decision) so an overridden deploy still says what it overrode.
func deployStartedBody(branch, override string) string {
	body := DeployStartedHeader
	if branch != "" {
		body += "\nbranch: " + branch
	}
	return body + override
}
