package daemon

import (
	"context"
	stderrors "errors"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// ErrDeployAwaitingDecision is the deploy's first NON-FAILURE refusal: the
// ticket is paused on an open [human:options] block, which is the single state
// nothing downstream may move. It is a sentinel rather than a message so a
// caller can tell it apart from a deploy that actually broke — a refusal must
// never be reported as a crash, must never post [human:deploy-failed], and must
// never red the card.
var ErrDeployAwaitingDecision = stderrors.New("the ticket is waiting on a decision")

// ErrDeployVerdictBlocks is the second non-failure refusal: the ticket's current
// verification verdict is a blocking one, so the change has to be rebuilt and
// re-reviewed before it may ship. The board's own route has refused this since
// the verdict gate existed (applyTransition); the CLI route used to walk past
// it, which is one of the two ways SC-4406 reached main unreviewed.
var ErrDeployVerdictBlocks = stderrors.New("the review verdict blocks the deploy")

// ErrDeployReviewUnavailable is the third non-failure refusal: this process
// cannot launch the machine pull-request reviewer, so the review the merge
// depends on cannot run. Only `human deploy --ready` — a person judging the
// change reviewed enough — ships from such a process, and it is recorded as an
// override rather than passed off as a reviewed merge.
var ErrDeployReviewUnavailable = stderrors.New("the machine pull-request review cannot run here")

// ErrDeployCheckoutBusy is the fourth non-failure refusal, and the only one
// about the machine rather than the ticket's own state: the implementation
// container for this ticket is still alive in the checkout the deploy pushes
// from. The deploy WAITS for it first (awaitCheckoutFree) and returns this only
// once DeployCheckoutWaitBound is spent, so reaching it means the container is
// hung rather than merely finishing — which is the stuck-running sweep's to
// answer, not the deploy's. Like its three siblings it posts no marker and reds
// no card (SC-5691).
var ErrDeployCheckoutBusy = stderrors.New("the implementation container still holds the checkout")

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

// DeployOutcome says how far StartDeploy carried the work. The outcomes end in
// different places — one on main, one in a review whose approval will put it
// there, one with a mechanical fixer resolving a stale base before any review
// runs — and a caller reporting to a person has to say which.
type DeployOutcome int

const (
	// DeployOutcomeShipped means the deploy engine ran to its end: the branch
	// merged, or was already on the base.
	DeployOutcomeShipped DeployOutcome = iota
	// DeployOutcomeReviewStarted means the branch is pushed, its pull request is
	// open in draft, and the machine reviewer owns it. The review→fix loop
	// un-drafts and merges the PR when it approves; nothing merged yet.
	DeployOutcomeReviewStarted
	// DeployOutcomeFixDispatched means the branch's base had advanced and the
	// merge conflicted, or merged clean but left the fast test tier red: the
	// deploy fixer was dispatched to resolve it BEFORE any reviewer ran, and no
	// review round was spent (SC-5279). The fixer hands back to the reviewer on
	// its own done exit; reporting this as "review started" would tell the
	// caller a reviewer is working when a fixer is (SC-5279 review round 1).
	DeployOutcomeFixDispatched
)

// StartDeployResult reports where StartDeploy left the work.
type StartDeployResult struct {
	Outcome DeployOutcome
	// PRURL and PRNumber identify the pull request the review loop holds; set
	// for DeployOutcomeReviewStarted and DeployOutcomeFixDispatched.
	PRURL    string
	PRNumber int
}

// StartDeploy is the deploy stage's single entry point for any route that STARTS
// a deploy from outside the board's own transition path — today `human deploy`.
// It owes the ticket the gates the engine cannot know about: the refusal while
// a decision is open, the refusal on a blocking verdict, the record that the
// work began, and the pull-request review that fronts every autonomous merge.
// The board's Deploy drop fronts the merge with the review loop (runDoneStage);
// this route used to skip it and hand the engine a mergeable PR, so a change
// that never touched the board reached main with CI as its only reader
// (SC-4406, F10). It now enters the same loop: draft PR, reviewer, approval,
// merge — and reuses an approval only when the approval names the head it is
// about to ship.
//
// `--ready` (MergeDraftPR) is the person's override of the review interlock:
// it runs the engine directly, as before, and the start marker says so.
//
// The board's three in-flight calls to DeployBranch are deliberately NOT routed
// here: they are continuations of a deploy the Done stage already recorded as
// [human:pr-review-started], not starts. A new route that begins a deploy belongs
// here, not on the engine.
//
// It also owes the ticket the checkout interlock: this process must not push
// into the checkout while the implementation container that built the branch is
// still alive in it (SC-5691).
func (d BoardTransitionDeps) StartDeploy(ctx context.Context, req StartDeployRequest) (StartDeployResult, error) {
	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return StartDeployResult{}, errors.WrapWithDetails(err, "loading PM comments before the deploy", "pm", req.PMKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	override, err := d.deployOverrides(req, card)
	if err != nil {
		return StartDeployResult{}, err
	}
	// A blocking verdict is refused before anything is pushed, and without a
	// marker: the ticket already says what has to happen (rebuild, re-review),
	// and a deploy-failed on top would red a card that is correctly waiting for
	// its rework. Read through the card so a verdict an older round produced is
	// retired by the newer handoff exactly as the board retires it (SC-4958).
	if VerdictFailed(card.Verdict) {
		return StartDeployResult{}, errors.WrapWithDetails(ErrDeployVerdictBlocks,
			"deploy refused: the review verdict blocks the deploy — rebuild and re-review the change first",
			"pm", req.PMKey, "verdict", card.Verdict)
	}
	if !d.MergeDraftPR && !d.canReview() {
		return StartDeployResult{}, errors.WrapWithDetails(ErrDeployReviewUnavailable,
			"deploy refused: the machine pull-request review needs the running daemon — start it and re-run, or re-run with --ready to ship without the review",
			"pm", req.PMKey)
	}
	// The launch gate is asked BEFORE anything is recorded or pushed, like the
	// other two refusals: a host that cannot launch the reviewer is a condition
	// of the machine, not of the ticket, so it posts no marker and moves no
	// item — the same rule the staged launch keeps (SC-5108).
	if !d.MergeDraftPR && d.launchGateBlocked(ctx, req.PMKey, prReviewAgentStage) {
		return StartDeployResult{}, errors.WrapWithDetails(ErrDeployReviewUnavailable,
			"deploy refused: this host cannot launch the machine reviewer right now — see `human doctor` for the blocker, fix it and re-run, or re-run with --ready to ship without the review",
			"pm", req.PMKey)
	}
	// The checkout interlock, before the start marker and before anything is
	// pushed, like the three refusals above: a deploy that never began must not
	// record a start. It is NOT skipped by --ready — that flag overrides the
	// machine review, and the engine it runs writes to the same tree the
	// implementation container holds (SC-5691). nil is passed for the
	// queued-deploy record on purpose: this route answers its caller with
	// ErrDeployCheckoutBusy, so the wait has a reader without a comment, and both
	// fix skills document that this refusal posts no marker (SC-5878 AD3).
	if err := d.awaitCheckoutFree(ctx, req.PMKey, nil); err != nil {
		return StartDeployResult{}, err
	}
	if err := d.recordDeployStart(ctx, req, override); err != nil {
		return StartDeployResult{}, err
	}
	if d.MergeDraftPR {
		return StartDeployResult{Outcome: DeployOutcomeShipped}, d.DeployBranch(ctx, req.PMKey, req.Title, req.PRBody, req.Branch)
	}
	return d.reviewThenShip(ctx, req, comments)
}

// deployOverrides collects the override lines the start marker must carry, and
// refuses on the open decision when nothing overrides it. Each override is a
// person walking past a guard, and the marker is the only place that walk is
// recorded — openOptionsBlock retires the open block on ANY later
// BoardRunning-classified marker, and deploy-started is one, so an override
// that leaves no trace would make the board silently forget the question was
// ever asked.
func (d BoardTransitionDeps) deployOverrides(req StartDeployRequest, card BoardCard) (string, error) {
	override := ""
	if awaitingDecision(card) {
		if !req.OverrideDecision {
			return "", errors.WrapWithDetails(ErrDeployAwaitingDecision,
				"deploy refused: this ticket is waiting on a decision — answer the open [human:options] block, or re-run with --override-decision",
				"pm", req.PMKey, "stage", string(card.OptionsStage))
		}
		override += "\noverride: deployed with an open decision on stage " + string(card.OptionsStage) +
			" — " + card.OptionsContext
	}
	if d.MergeDraftPR {
		override += "\noverride: shipped with --ready — the machine pull-request review was not waited for"
	}
	return override, nil
}

// recordDeployStart posts the start marker. A plain start is best-effort,
// exactly like the PR loop's converging marker: the merge is the work, and
// refusing to ship over a lost sentence trades code for a comment. An override
// is different: its line is the ONLY record that a ship walked past a guard,
// nothing has been pushed yet, so failing closed costs nothing and shipping an
// unrecorded override is exactly what the line exists to prevent.
func (d BoardTransitionDeps) recordDeployStart(ctx context.Context, req StartDeployRequest, override string) error {
	_, err := d.Commenter.AddComment(ctx, req.PMKey, deployStartedBody(req.Branch, override))
	if err == nil {
		return nil
	}
	if override != "" {
		return errors.WrapWithDetails(err, "posting the deploy-started override record; refusing to ship an unrecorded override",
			"pm", req.PMKey)
	}
	d.Logger.Warn().Err(err).Str("pm", req.PMKey).
		Msg("deploy: could not record the start on the ticket; continuing to the gate")
	return nil
}

// canReview reports whether this process can launch the machine reviewer at
// all. The CLI run without a daemon builds its deps without a launcher, and a
// deploy that cannot run the review must not pretend it did.
func (d BoardTransitionDeps) canReview() bool {
	return d.Launcher != nil
}

// reviewThenShip is the CLI route's entry into the review→fix loop. It pushes
// the branch and opens (or adopts) its pull request in draft, then either
// reuses a still-current approval of exactly this head — un-drafting and
// running the engine, as the loop's own merge step does — or launches the
// reviewer and returns, leaving the loop to merge on approval.
func (d BoardTransitionDeps) reviewThenShip(ctx context.Context, req StartDeployRequest, comments []tracker.Comment) (StartDeployResult, error) {
	shipped := StartDeployResult{Outcome: DeployOutcomeShipped}
	// The already-merged carve-out is the engine's: it records the terminal
	// marker and closes the ticket, and there is nothing to review.
	if d.Deployer.BranchMerged(ctx, d.WorkspaceDir, req.Branch) {
		return shipped, d.DeployBranch(ctx, req.PMKey, req.Title, req.PRBody, req.Branch)
	}
	res, err := d.openDraftPR(ctx, req.PMKey, req.Branch, req.Title, req.PRBody)
	if err != nil {
		return shipped, err
	}
	if d.approvalCoversPR(ctx, comments, req.Branch, res) {
		if res.Draft {
			if err := d.Deployer.MarkReadyForReview(ctx, d.WorkspaceDir, res.Number); err != nil {
				return shipped, d.deployFailed(req.PMKey, res.URL, deployReason(
					"the reviewed PR could not be marked ready for merge — open the PR and mark it ready, then re-run Deploy", err))
			}
		}
		d.Logger.Info().Str("pm", req.PMKey).Int("pr", res.Number).
			Msg("deploy: the machine review already approved this head; shipping without a new round")
		return shipped, d.DeployBranch(ctx, req.PMKey, req.Title, req.PRBody, req.Branch)
	}
	dispatched, err := d.launchPRReview(ctx, req.PMKey, res, req.Branch)
	if err != nil {
		// A gate that went red between the pre-check above and this launch
		// surfaces as ErrLaunchGateRefused: a host condition, so no marker — the
		// caller reports it and the item is left where the start marker put it.
		return shipped, err
	}
	if dispatched {
		// The base merge conflicted, or merged clean but left the fast test
		// tier red: the deploy fixer owns this step, not a reviewer — its
		// marker stands and its done exit hands back to the reviewer
		// (SC-5279). Reporting DeployOutcomeReviewStarted here would tell the
		// caller a reviewer is working when the fixer is.
		return StartDeployResult{Outcome: DeployOutcomeFixDispatched, PRURL: res.URL, PRNumber: res.Number}, nil
	}
	// dispatched == false with no error is a reviewer owning this step on this
	// machine — freshly launched here, or already running from an earlier
	// call — so the review is, truthfully, started.
	return StartDeployResult{Outcome: DeployOutcomeReviewStarted, PRURL: res.URL, PRNumber: res.Number}, nil
}

// approvalCoversPR reports whether a still-current machine approval judged the
// exact head the pull request now carries. An approval is evidence about one
// revision of one branch, so it is reused only when the approval names the
// branch and the head, no later review round or handoff has superseded it, and
// the forge reports that head on the PR. Anything less — an older marker with
// no head, a moved branch, an unreadable PR — runs the review; a start marker,
// a non-draft PR or a passing verification verdict is not PR-review approval.
func (d BoardTransitionDeps) approvalCoversPR(ctx context.Context, comments []tracker.Comment, branch string, res PRResult) bool {
	head, ok := currentApproval(comments, branch)
	if !ok {
		return false
	}
	state, err := d.Deployer.ReadPullRequest(ctx, d.WorkspaceDir, res.Number)
	if err != nil || state == nil {
		return false
	}
	return strings.TrimSpace(state.HeadSHA) == head
}

// currentApproval returns the head a [human:pr-review-passed] marker approved
// for branch, when that approval is the newest word on the review: a later
// review round (pr-review-started) means the work moved on, and a later
// handoff does too — unless that handoff is a bookkeeping repost, posted
// AFTER a verdict to record what the branch already holds, naming nothing
// that verdict did not judge (SC-5475; handoffIsBookkeepingRepost). An
// ordinary rework's handoff precedes the verdict it results in and so still
// voids the stale approval. The head the caller then compares against the
// pull request is what binds the reuse to one revision; this only decides
// whether the approval is still the newest word.
func currentApproval(comments []tracker.Comment, branch string) (head string, ok bool) {
	passed, found := latestCommentWithHeader(comments, PRReviewPassedHeader)
	if !found {
		return "", false
	}
	if later, has := latestCommentWithHeader(comments, PRReviewStartedHeader); has && commentNewer(later, passed) {
		return "", false
	}
	if later, has := latestCommentWithHeader(comments, ReadyForReviewHeader); has &&
		commentNewer(later, passed) && !handoffIsBookkeepingRepost(comments) {
		return "", false
	}
	m, parsed := marker.ParseBody(passed.Body)
	if !parsed {
		return "", false
	}
	head = strings.TrimSpace(m.Fields["head"])
	if head == "" || strings.TrimSpace(m.Fields["branch"]) != branch {
		return "", false
	}
	return head, true
}

// deployStartedBody names the branch on the start marker so a reader can tell
// WHICH branch a deploy carried without waiting for the merge to say so, and
// carries the override lines (empty unless the deploy walked past a guard) so
// an overridden deploy still says what it overrode.
func deployStartedBody(branch, override string) string {
	body := DeployStartedHeader
	if branch != "" {
		body += "\nbranch: " + branch
	}
	return body + override
}
