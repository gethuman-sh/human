package daemon

import (
	"context"
	stderrors "errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/agentname"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// ErrAgentAlreadyRunning is the AgentLauncher-boundary sentinel for a benign
// single-flight refusal: the stage's agent is already running, so a racing
// retry must be a no-op rather than a failure. The daemon package cannot import
// internal/agent (that would cycle — agent imports daemon), so the launcher
// implementation in cmd/cmddaemon translates agent.ErrAlreadyRunning into this
// contract sentinel at the boundary (SC-1419).
var ErrAgentAlreadyRunning = stderrors.New("agent already running")

// AgentLauncher launches a containerized agent for a board stage. It is an
// interface so the transition engine is testable without Docker. An
// implementation returns ErrAgentAlreadyRunning when the stage's agent is
// already running so the caller can treat the racing retry as a no-op.
type AgentLauncher interface {
	// runID is the token the daemon minted for this launch; the implementation
	// injects it into the container so every hook event the run fires carries it
	// back and the daemon can recognise its own work (SC-4082). Empty means the
	// launch was not registered and the run reports no id.
	Launch(ctx context.Context, name, prompt, workspace, configDir, runID string) error
}

// Deployer executes the forge side of the deploy pipeline: push + PR, the CI
// gate, the merge, and branch cleanup. Injected so the Done stage is testable
// without git/forge access.
type Deployer interface {
	PushAndCreatePR(ctx context.Context, req PRRequest) (PRResult, error)
	PullRequestChecks(ctx context.Context, workspaceDir string, number int) (forge.ChecksState, error)
	// ReadPullRequest reads the PR's full state and per-check results — the
	// richer surface the failure/timeout headlines name the offending checks
	// from. Best-effort on those paths: a read failure degrades the headline to
	// its bare reason, never inverts the gate verdict.
	ReadPullRequest(ctx context.Context, workspaceDir string, number int) (*forge.PullRequestState, error)
	// EnsureMergeable makes the handoff branch current with the base before the
	// merge is attempted: it verifies the PR is mergeable against current main
	// and, when it is not, rebases the branch, re-pushes (lease), and re-verifies.
	// A returned error is a real conflict the mechanical path cannot resolve — the
	// deploy must NOT attempt the merge blind, but fail loudly instead.
	// rebased reports whether the branch was rewritten and re-pushed: the forge
	// then recomputes the PR's mergeability asynchronously, and merging inside
	// that window draws a spurious 405 — the caller must wait it out first.
	EnsureMergeable(ctx context.Context, req PRRequest) (rebased bool, err error)
	// PullRequestMergeable reports the forge's own end-state (three-way) merge
	// verdict for the PR. It is the fallback signal when the mechanical rebase in
	// EnsureMergeable conflicts on an intermediate commit the end-state merge
	// never sees (SC-804).
	PullRequestMergeable(ctx context.Context, workspaceDir string, number int) (bool, error)
	MergePullRequest(ctx context.Context, workspaceDir string, number int) error
	DeleteRemoteBranch(ctx context.Context, workspaceDir, branch string) error
	// BranchMerged reports whether the branch's work is already contained in the
	// base branch (an ancestor of origin/<base>). A re-run Deploy on a finished
	// card must short-circuit to a clean no-op rather than open a doomed PR the
	// forge rejects 422 "No commits between" (SC-911).
	BranchMerged(ctx context.Context, workspaceDir, branch string) bool
	// MarkReadyForReview converts the draft PR opened for the review loop to
	// ready-for-review, so the adopted PR can merge once the machine review approves.
	MarkReadyForReview(ctx context.Context, workspaceDir string, number int) error
	// PublishResolvedBranch publishes a conflict resolution the deploy-fixer left
	// on the LOCAL branch ref. Board agents hold no push credentials, so the
	// daemon — which does — is what carries their work to the forge; without this
	// the resolution is unreachable, because the deploy reads the branch from
	// origin and would re-run the same conflicting rebase (SC-2845). It reports
	// whether it published: a local ref that is absent, unchanged, or does not
	// yet contain the base tip is no resolution, and is left for the deploy's own
	// freshness rebase to handle.
	PublishResolvedBranch(ctx context.Context, workspaceDir, branch string) (published bool, err error)
}

// PRRequest carries everything needed to push a branch and open its PR.
type PRRequest struct {
	WorkspaceDir string
	Branch       string
	Title        string
	Body         string
	// Draft opens the PR in the forge's draft (unmergeable) state — the review
	// loop opens draft, then un-drafts on approval so the reviewed PR can merge.
	Draft bool
}

// PRResult identifies the created pull request for the pipeline steps that
// follow creation (checks, merge).
type PRResult struct {
	URL    string
	Number int
	// Draft reports that the pull request the gate is about to ship is in the
	// forge's draft state. It is carried out of PR creation/adoption because the
	// deploy gate cannot otherwise know: the review loop opens its PR draft on
	// purpose, and an adopted PR is returned as-is, so a deploy driven by anything
	// other than the loop's own approval reaches the merge with no idea the change
	// is still held for review (SC-4027).
	Draft bool
}

// deployWaitHeartbeat is how many CI polls pass between "still running" log
// lines — five minutes at the default interval. Often enough that a live deploy
// is visibly live, rare enough that a 45-minute wait does not bury the log.
const deployWaitHeartbeat = 10

// Deploy pacing. Package vars so tests can run the CI gate without real time.
var (
	deployCheckInterval = 30 * time.Second
	deployTimeout       = 45 * time.Minute
	// Mergeability-recompute pacing: after a freshness rebase re-pushes the
	// branch, the forge recomputes the PR's mergeability asynchronously and the
	// merge endpoint 405s until it settles (ticket 910's deploy hit exactly
	// this). The poll waits for a definitive verdict before merging.
	mergeablePollInterval = 3 * time.Second
	mergeablePollTimeout  = 60 * time.Second
	// Merge-retry pacing: even past the mergeability recompute the forge can
	// report the rebased head unstable/behind for a beat (or a concurrent deploy
	// advances the base under it), 405-ing the merge with a transient "not
	// mergeable". The bounded retry rides that window out instead of dead-ending
	// the card (SC-1184).
	mergeRetryInterval = 3 * time.Second
	mergeRetryTimeout  = 60 * time.Second
)

// BoardTransitionRequest is the wire request for advancing a card one stage.
// PMTitle is carried from the card so the Done stage can title the PR without a
// second tracker fetch.
type BoardTransitionRequest struct {
	PMKey   string     `json:"pm_key"`
	PMTitle string     `json:"pm_title"`
	From    BoardStage `json:"from"`
	To      BoardStage `json:"to"`
	// Cause names what filled the gap before this launch so an over-threshold
	// inter-stage wait can be recorded and attributed (SC-2462). Empty = a
	// human-initiated drop, whose interval is deliberation, not a pipeline wait,
	// and is never recorded.
	Cause WaitCause `json:"cause,omitempty"`
	// Reopen restarts a stage the pipeline RESOLVED — a [human:nothing-to-do] or
	// [human:no-fix-needed] terminal a person judges wrong.
	//
	// It is a separate flag rather than another state in the retry predicates
	// because the automatic relaunch drives the very same path (StageRetry's
	// Relaunch calls this request with From == To). Widening isBuildRetry or
	// isPlanningRetry to accept resolved would hand the machine permission to
	// re-run its own clean terminals forever, which is precisely what "never red,
	// never retried" exists to prevent. Only a human sets this.
	//
	// Without it a resolved card was unrecoverable: no gesture moved it, because
	// the retry paths key on failed or outage and the forward path requires done.
	// A wrong not-a-bug verdict could only be undone by editing the tracker by
	// hand — which is why the verdict must survive an adversarial challenge
	// before it is trusted at all.
	Reopen bool `json:"reopen,omitempty"`
}

// BoardTransitionDeps wires the transition engine's collaborators.
type BoardTransitionDeps struct {
	Commenter tracker.Commenter
	Launcher  AgentLauncher
	Deployer  Deployer
	// CloseTicket closes the PM ticket after a successful deploy so shipped
	// work leaves the board. nil skips the close (the deploy still succeeds).
	CloseTicket func(pmKey string) error
	// SetTicketOwner makes this machine the PM ticket's owner when a stage launches,
	// so the board can show who holds a card (SC-3345). nil skips the claim — an
	// un-wired daemon still runs every stage, it just records no ownership.
	SetTicketOwner func(pmKey string) error
	WorkspaceDir   string
	ConfigDir      string
	// DaemonID stamps this daemon's identity on every marker it posts, as the
	// machine: field the signing commenter injects at the write choke point.
	// Empty leaves markers un-signed (the signer's empty-machine no-op), so an
	// un-provisioned daemon still functions.
	DaemonID string
	// Runs is the daemon's record of the runs it launched, so the hook path can
	// act on its own work rather than on whatever an event names (SC-4082). nil
	// leaves runs unregistered and the exit path on its pre-registry behaviour.
	Runs *RunRegistry
	// MergeDraftPR authorizes the deploy gate to un-draft a pull request the
	// machine review loop is still holding, and ship it. It is a person saying "I
	// have judged this reviewed enough", so it is set only from an explicit
	// gesture (`human deploy --ready`) and never by an automatic path: the draft is
	// the interlock that stops a half-reviewed change merging when the daemon's own
	// gate fails, and a machine that could clear its own interlock has none.
	MergeDraftPR bool
	// Logger records best-effort post-merge failures (e.g. a failed automated
	// close) and the deploy gate's progress. The zero value is a safe no-op
	// writer, so an un-wired path stays valid without a logger — but wire one
	// for any path that can deploy: a gate that sits ten minutes on CI and is
	// then interrupted leaves no other evidence it ever ran, which is exactly
	// the state that has to be reconstructed from merge timestamps afterwards.
	Logger zerolog.Logger
	// Diagnose distills why a dead run died, so a loop step that escalated
	// without recording an outcome reports the real cause instead of a generic
	// line. nil disables diagnosis (the package's "nil disables" convention).
	Diagnose BoardFailureDiagnoser
	// LaunchGate reports the launch-critical doctor checks currently failing on
	// this daemon's host (docker, agent-skills, claude-auth). When it returns a
	// non-empty slice the stage launcher neither claims nor launches — it silently
	// leaves the work for a healthy daemon, and the failure surfaces only on this
	// host (doctor / rail LED), never as a ticket marker (SC-912). nil disables.
	LaunchGate func(ctx context.Context) []DoctorCheck
	// BlockedBy reports the still-open issues pmKey must wait for. It resolves
	// each blocker's real status, so a finished blocker is simply absent from
	// the result — the gate never has to guess what "open" means. nil disables
	// the gate (the package's "nil disables" convention).
	BlockedBy func(ctx context.Context, pmKey string) ([]string, error)
	// Getter fetches the PM ticket so a recovery relaunch of the implementation
	// stage can tell a self-planning fix pipeline (bug/security — which produces
	// its own plan within the run) from a plan-executing build, and re-dispatch
	// the right path. nil disables kind classification: the relaunch then falls
	// back to the [human:bug-verdict] marker heuristic and finally to the plain
	// build retry (SC-2986).
	Getter tracker.Getter
}

// Hyphen-free agent-name suffixes for the PR review→fix loop steps. parseAgentName
// splits on the last hyphen, so the token itself must carry none — the public
// marker/state names keep their hyphenated form (pr-review-started, stage.pr-review);
// only the internal agent-name token is hyphen-free.
const (
	prReviewAgentStage  BoardStage = "prreview"
	prFixAgentStage     BoardStage = "prfix"
	deployFixAgentStage BoardStage = "deployfix"
)

// agentNameFor builds the agent name for a board stage. The grammar lives in
// internal/agentname, which internal/capabilities reads too; these two wrappers
// exist only to carry BoardStage across it.
func agentNameFor(pmKey string, stage BoardStage) string {
	return agentname.Board(pmKey, string(stage))
}

// parseAgentName recovers the PM key and stage from a board agent name. The PM
// key is returned sanitized (the form embedded in the name), which is
// sufficient to re-resolve comments since the daemon fetched the same keys.
func parseAgentName(name string) (pmKey string, stage BoardStage, ok bool) {
	key, s, ok := agentname.ParseBoard(name)
	return key, BoardStage(s), ok
}

// ApplyTransition advances a card from its current stage to the requested next
// stage. The daemon re-loads live comments and re-derives the card here because
// the UI gate is advisory only — the daemon is the authority on whether a
// forward move is allowed (forward-only, single-step, gated on the prior
// stage's completion). All errors carry details for the client.
func (d BoardTransitionDeps) ApplyTransition(ctx context.Context, req BoardTransitionRequest) error {
	_, err := d.applyTransition(ctx, req)
	return err
}

// ApplyRetryTransition is the automatic-retry entry: it additionally reports
// whether a launch actually happened, so the retry accounting never charges an
// attempt for a refusal that started nothing (SC-2989).
func (d BoardTransitionDeps) ApplyRetryTransition(ctx context.Context, req BoardTransitionRequest) (launched bool, err error) {
	return d.applyTransition(ctx, req)
}

// applyTransition is the shared body behind both public entries. It reports
// whether a launch actually happened alongside the error so the retry path can
// tell a genuine relaunch from a refusal that started nothing (SC-2989); the
// error-only ApplyTransition wrapper discards launched for every drag/gesture/
// chain caller that only cares whether the move was accepted.
func (d BoardTransitionDeps) applyTransition(ctx context.Context, req BoardTransitionRequest) (launched bool, err error) {
	// Ideas never move via board transitions: promotion out of the Ideas
	// column is a label swap performed by the ideation engine's evolve mode,
	// which the desktop opens instead of calling this route.
	if req.From == BoardIdeas || req.To == BoardIdeas {
		return false, errors.WithDetails("ideas transitions are handled via ideation",
			"pm", req.PMKey, "from", string(req.From), "to", string(req.To))
	}

	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return false, errors.WrapWithDetails(err, "loading PM comments for transition", "pm", req.PMKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	// Idempotency, checked first because a re-drop derives the card as already
	// sitting in the target stage, which the forward-only rule below would
	// otherwise reject as a non-advance.
	if isDuplicateDrop(req.To, card) {
		return false, nil
	}

	// A card paused on an open [human:options] decision has exactly one valid
	// next move — choosing an option (ApplyOption), a click, never a drag. Refuse
	// every drop on it with a reason the user can act on, rather than letting the
	// forward-only rule below reject it with an opaque "not the single next stage"
	// (or, for the done-stage PR-loop escalation before SC-1857 paused it, swallow
	// the drop as a silent duplicate). The returned error is what the board surfaces
	// as its refusal banner: a refused move must say why, never appear to do nothing.
	if awaitingDecision(card) {
		return false, errors.WithDetails(
			"this card is waiting on a decision — choose an option before moving it",
			"pm", req.PMKey, "stage", string(card.Stage), "to", string(req.To))
	}

	// Sanctioned non-forward moves — the rework backward step and the in-place
	// stage retries — are dispatched before the forward-only rule, which would
	// otherwise reject each as a non-advance. Extracted so applyTransition reads
	// as guards → sanctioned-non-forward → forward, one concern per block.
	if handled, launched, err := d.dispatchNonForwardMove(ctx, req, card, comments); handled {
		return launched, err
	}

	// Forward-only, single-next-stage: the target must be exactly one rank
	// above the current derived stage.
	if stageRank[req.To] != stageRank[card.Stage]+1 {
		return false, errors.WithDetails("transition is not the single next stage",
			"pm", req.PMKey, "current", string(card.Stage), "to", string(req.To))
	}

	// Gating: every boundary except Backlog→Planning requires the prior stage
	// to have completed (done-marker present).
	if card.Stage != BoardBacklog && card.State != BoardDone {
		return false, errors.WithDetails("prior stage not complete",
			"pm", req.PMKey, "stage", string(card.Stage), "state", string(card.State))
	}

	// A failing review verdict blocks the deploy: the card must be rebuilt
	// (rework loop) and re-reviewed before it can ship.
	if req.To == BoardDoneStage && VerdictFailed(card.Verdict) {
		return false, errors.WithDetails("review verdict blocks deploy",
			"pm", req.PMKey, "verdict", card.Verdict)
	}

	return d.launchForwardStage(ctx, req, card)
}

// dispatchNonForwardMove handles the sanctioned moves that are not a single
// forward step: the one allowed backward move (rework after a failing review)
// and the in-place stage retries (planning, build, review, deploy). Each targets
// a stage the card already derives to, which the forward-only rule would reject
// as a non-advance, so they are resolved here first. handled reports whether the
// request matched one of them — when false, ApplyTransition falls through to the
// forward-only path — and err carries that dispatch's own result.
func (d BoardTransitionDeps) dispatchNonForwardMove(ctx context.Context, req BoardTransitionRequest, card BoardCard, comments []tracker.Comment) (handled bool, launched bool, err error) {
	switch {
	// Queued launch: a decision was answered and the stage it named has not
	// started. The card derives to (that stage, queued) — a state none of the
	// retry rules below admits, so this fell through to the forward-only rule and
	// was rejected as a non-advance. That made reconcileQueuedLaunch, the pass
	// added to start exactly these cards (SC-3865), unable to start any of them:
	// every attempt was refused, charged against the retry budget, and the card
	// left where it was. Dispatched first because a queued card matches this rule
	// and no other, and because it is also the human override for a card held
	// behind a ticket it was told to wait for.
	case isQueuedLaunch(req.To, card):
		launched, err := d.launchDecidedStage(ctx, req.PMKey, req.To, card, comments, choiceLabel(comments))
		return true, launched, err

	// Rework loop: a build whose review failed may be rebuilt. This is the ONE
	// sanctioned backward move — the executor is re-dispatched with the review
	// findings, and the resulting handoff chains into a fresh review.
	//
	// A fix-built card carries no [human:plan]: the self-planning fix pipeline
	// derives its plan within the run. Routing its rework through the plan
	// executor would refuse it at the plan gate for having no plan — the exact
	// SC-2986 hole, here on the rework path — so it is classified first and
	// re-dispatched to its own fix pipeline, which re-derives the plan (SC-2989).
	case isReworkTransition(req.To, card):
		switch d.classifyFixPipeline(ctx, req.PMKey, comments) {
		case fixBug:
			err := d.ApplyFix(ctx, BoardFixRequest{PMKey: req.PMKey, PMTitle: req.PMTitle})
			return true, err == nil, err
		case fixSecurity:
			err := d.ApplySecurityFix(ctx, SecurityFixRequest{PMKey: req.PMKey, PMTitle: req.PMTitle})
			return true, err == nil, err
		}
		launched, err := d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			executePrompt(dispatchKey(req.PMKey, card),
				" — a review found problems; address the findings in the latest [human:review-complete] comment on the ticket first"),
			WaitCauseChain, true)
		return true, launched, err

	// Re-open: a person judged a resolved terminal wrong. Dispatched before the
	// retry rules because a resolved card matches none of them by design — the
	// machine must never re-run its own clean terminal, and only this explicitly
	// human flag reaches here.
	case req.Reopen && card.State == BoardResolved:
		launched, err := d.reopenResolved(ctx, req, card, comments)
		return true, launched, err

	// Planning retry: a failed planning run is relaunched in place. The retry
	// gesture targets planning while the card already derives to planning, so
	// the single-step rule would reject it and the gesture would launch nothing
	// (SC-355). A RUNNING planning card never reaches this path — the idempotency
	// guard already returned for it.
	case isPlanningRetry(req.To, card):
		launched, err := d.startAgentStage(ctx, req.PMKey, BoardPlanning, PlanningStartedHeader,
			planPrompt(req.PMKey), WaitCauseRetry, false)
		return true, launched, err

	// Build retry: the same sanctioned in-place relaunch for a failed
	// implementation run — without it a failed build is a dead end, since the
	// rework re-drop requires a failed REVIEW verdict and Retry fix is
	// bug-pane-only (SC-591).
	//
	// A self-planning fix relaunch: an autofix/security-fix run interrupted
	// mid-run (its implementation stage failed or hit an outage) must resume as
	// the fix pipeline, which produces its own plan — NOT the plan-executing
	// build retry, whose plan gate would refuse the run and ask a human to run
	// planning the pipeline runs itself (SC-2986). A plan-executing build has an
	// intact plan on the ticket; a fresh executor picks it up.
	case isBuildRetry(req.To, card):
		switch d.classifyFixPipeline(ctx, req.PMKey, comments) {
		case fixBug:
			err := d.ApplyFix(ctx, BoardFixRequest{PMKey: req.PMKey, PMTitle: req.PMTitle})
			return true, err == nil, err
		case fixSecurity:
			err := d.ApplySecurityFix(ctx, SecurityFixRequest{PMKey: req.PMKey, PMTitle: req.PMTitle})
			return true, err == nil, err
		}
		launched, err := d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			executePrompt(dispatchKey(req.PMKey, card), ""), WaitCauseRetry, true)
		return true, launched, err

	// Review retry: a stage-failed review is otherwise a dead end. The rework
	// re-drop keys on a DONE verification with a failing verdict, and a
	// [human:review-failed] card (state failed) matches neither it nor any
	// forward move — so a failed binding gate (missing branch, unreachable
	// commits) could never be retried. Relaunch the review in place, re-bound to
	// the same handoff (SC-695). A RUNNING review is caught by the idempotency guard.
	case isReviewRetry(req.To, card):
		launched, err := d.startAgentStage(ctx, req.PMKey, BoardVerification, ReviewStartedHeader,
			reviewPrompt(dispatchKey(req.PMKey, card), card), WaitCauseRetry, false)
		return true, launched, err

	// Deploy retry: a card sitting on a failed deploy, re-dropped on Deploy, must
	// re-run the deploy pipeline — the freshness stage rebases the already-reviewed
	// branch and re-attempts the merge. Without this the forward-only rule rejects
	// the same-stage move and a conflicted deploy is a dead end that can only be
	// escaped by re-implementing already-reviewed work (735).
	case isDeployRetry(req.To, card):
		err := d.runDoneStage(ctx, req, card)
		return true, err == nil, err
	}
	return false, false, nil
}

// reopenResolved restarts the stage that resolved the card, on a person's say-so.
//
// It relaunches the same stage the terminal was posted in: a
// [human:nothing-to-do] card re-plans, and a [human:no-fix-needed] card re-runs
// the fix — through its own self-planning pipeline where it has one, so an
// autofix run that wrongly concluded not-a-bug re-triages rather than being
// handed to the plan executor, which would refuse it for having no plan.
//
// The relaunch's *-started marker is strictly newer than the terminal, so the
// card leaves resolved by the derivation's ordinary rules; nothing needs to
// retract the terminal, and the trail keeps both the verdict and the decision to
// overrule it.
func (d BoardTransitionDeps) reopenResolved(ctx context.Context, req BoardTransitionRequest, card BoardCard, comments []tracker.Comment) (bool, error) {
	if card.Stage == BoardPlanning {
		return d.startAgentStage(ctx, req.PMKey, BoardPlanning, PlanningStartedHeader,
			planPrompt(req.PMKey)+" — a person re-opened this ticket after it was resolved as nothing to do;"+
				" re-examine it rather than repeating the earlier conclusion",
			WaitCause(""), false)
	}
	switch d.classifyFixPipeline(ctx, req.PMKey, comments) {
	case fixBug:
		err := d.ApplyFix(ctx, BoardFixRequest{PMKey: req.PMKey, PMTitle: req.PMTitle})
		return err == nil, err
	case fixSecurity:
		err := d.ApplySecurityFix(ctx, SecurityFixRequest{PMKey: req.PMKey, PMTitle: req.PMTitle})
		return err == nil, err
	}
	return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
		executePrompt(dispatchKey(req.PMKey, card),
			" — a person re-opened this ticket after it was resolved as needing no fix"),
		WaitCause(""), true)
}

// launchForwardStage dispatches an already-sanctioned forward transition to
// its stage launcher. Split from ApplyTransition so the gate chain and the
// dispatch read (and count) as separate concerns.
func (d BoardTransitionDeps) launchForwardStage(ctx context.Context, req BoardTransitionRequest, card BoardCard) (launched bool, err error) {
	switch req.To {
	case BoardPlanning:
		return d.startAgentStage(ctx, req.PMKey, BoardPlanning, PlanningStartedHeader,
			planPrompt(req.PMKey), req.Cause, false)
	case BoardImplementation:
		return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			executePrompt(dispatchKey(req.PMKey, card), ""), req.Cause, true)
	case BoardVerification:
		return d.startAgentStage(ctx, req.PMKey, BoardVerification, ReviewStartedHeader,
			reviewPrompt(dispatchKey(req.PMKey, card), card), req.Cause, false)
	case BoardDoneStage:
		err := d.runDoneStage(ctx, req, card)
		return err == nil, err
	default:
		return false, errors.WithDetails("unsupported transition target", "to", string(req.To))
	}
}

// BoardFixRequest is the wire request for launching the autonomous bug-fix
// pipeline on a bug ticket. PMTitle is carried like BoardTransitionRequest's so
// downstream stages never need a second tracker fetch.
type BoardFixRequest struct {
	PMKey   string `json:"pm_key"`
	PMTitle string `json:"pm_title"`
}

// ApplyFix launches the autonomous bug-fix pipeline (/human-autofix) on a bug
// ticket. Bugs skip the board's planning gate — autofix triages, plans and
// fixes in one run — so this is a separate entry point rather than a relaxation
// of ApplyTransition's forward-only rule. The agent is named exactly like a
// board implementation stage, so the failure watcher and the build→review
// chain apply to a bug fix unchanged.
func (d BoardTransitionDeps) ApplyFix(ctx context.Context, req BoardFixRequest) error {
	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM comments for fix", "pm", req.PMKey)
	}
	// Idempotency: a re-drop or a Retry click while the fix agent — or the
	// review it chains into — is still running must not launch a second one.
	// This is stage-scoped (implementation, then the verification it chains
	// into) rather than a whole-card check on purpose: DeriveBoardCard reports
	// the FURTHEST stage's state, so a stale [human:deploy-failed] marker pins
	// the card to done/failed and structurally hides a running re-fix from a
	// whole-card guard (SC-230). Deliberately NOT the derived-card guard
	// ApplyTransition uses: these two stages carry no supersede semantics, so a
	// raw scan is both accurate here and immune to that masking.
	if _, state := latestStageState(comments, BoardImplementation); state == BoardRunning {
		return nil
	}
	if _, state := latestStageState(comments, BoardVerification); state == BoardRunning {
		return nil
	}
	_, err = d.launchFixPipeline(ctx, req.PMKey, fixBug, "")
	return err
}

// launchFixPipeline starts the self-planning fix pipeline that owns a ticket and
// records which one it is. extra is appended to the prompt, so a run resumed by
// a decision carries the direction that resumed it.
//
// The --board marker is the mechanical gate that keeps a board run from pushing:
// the container holds no push/PR credentials, and the daemon's Deploy stage owns
// push → PR → CI → merge on the host against the bind-mounted repo. The skill
// and fixer branch on this flag to stop at the review handoff. Relying on the
// HUMAN_AGENT_NAME env var alone let a fixer push and fail — the fix completed
// and passed review but the card ended red (SC-252).
//
// These pipelines triage, plan and fix in one run, so they legitimately launch
// the implementation stage with no pre-written plan: requiresPlan is false
// (SC-2596).
//
// It carries NO idempotency guard of its own. The two gesture entry points
// (ApplyFix, ApplySecurityFix) check for a running stage before calling; the
// paths that resume a decision must not, because the agent that RAISED the
// decision leaves a [human:implementation-started] marker standing — a stage
// that pauses on an open block posts no *-failed marker (stagePausedOnOptions),
// so a marker-shaped guard reads the dead run as live and swallows the resume.
// Those paths establish liveness the honest way, from the running containers.
func (d BoardTransitionDeps) launchFixPipeline(ctx context.Context, pmKey string, kind fixPipeline, extra string) (bool, error) {
	skill, identity := "/human-autofix ", "fix"
	if kind == fixSecurity {
		skill, identity = "/human-security-fix ", "security"
	}
	launched, err := d.startAgentStage(ctx, pmKey, BoardImplementation, ImplementationStartedHeader,
		skill+pmKey+" --board"+extra, WaitCause(""), false)
	if launched {
		// Record which pipeline this run is, durably on the ticket, so a later
		// recovery relaunch restarts the SAME pipeline even if the ticket-kind
		// fetch blips and no verdict has been posted yet (SC-2989).
		_ = postMarker(ctx, d.Commenter, pmKey, marker.Marker{
			Type: MarkerPipeline, Fields: fields("kind", identity),
		})
	}
	return launched, err
}

// SecurityFixRequest is the wire request for launching the security-fix pipeline
// on a security ticket. It mirrors BoardFixRequest — PMTitle is carried so
// downstream stages never need a second tracker fetch.
type SecurityFixRequest struct {
	PMKey   string `json:"pm_key"`
	PMTitle string `json:"pm_title"`
}

// ApplySecurityFix launches the security-fix pipeline (/human-security-fix) on a
// security ticket. Like ApplyFix it skips the board's planning gate — the skill
// triages, plans and fixes in one run — and it launches the agent under the
// BoardImplementation stage name so the failure watcher and the build→review
// chain apply to a security fix unchanged. The only difference from ApplyFix is
// the skill invoked: a security-tuned triage/verify pass instead of autofix.
func (d BoardTransitionDeps) ApplySecurityFix(ctx context.Context, req SecurityFixRequest) error {
	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM comments for security fix", "pm", req.PMKey)
	}
	// Idempotency mirrors ApplyFix: a re-drop or Retry click while the fix agent
	// — or the review it chains into — is still running must not launch a second.
	if _, state := latestStageState(comments, BoardImplementation); state == BoardRunning {
		return nil
	}
	if _, state := latestStageState(comments, BoardVerification); state == BoardRunning {
		return nil
	}
	_, err = d.launchFixPipeline(ctx, req.PMKey, fixSecurity, "")
	return err
}

// startAgentStage launches the agent and, only when one actually started, posts
// the stage's started marker. On launch failure it posts the stage's *-failed
// marker so the board reflects the error rather than leaving a stuck spinner.
// cause names what filled the gap before this launch (SC-2462): a non-empty
// cause over StageWaitThreshold gets an attributed [human:stage-wait] record; an
// empty cause (a human-initiated drop) is deliberation, never recorded.
// launchAgent is the single AgentLauncher boundary every board launch path
// routes through. A benign single-flight refusal (ErrAgentAlreadyRunning) means
// "one is already working on it" on this machine, not "this failed", so it is
// still not an error here and the caller posts no failed marker — leaving the
// existing run's record standing. Every other error is returned unchanged so a
// launch that genuinely could not happen (no container, no credentials) still
// fails loudly. Centralizing the no-op contract here means a new launch path
// inherits it without rediscovering the rule (SC-2603; the per-call-site guard
// it replaces was SC-1419).
// The refusal is now reported rather than erased: launched is the fact every
// caller needs before it may claim a run happened, because a marker that
// outlives its own launch re-dates the card's clock — buying the still-running
// agent another StuckRunningGrace from the pass meant to reach it — and charges
// the retry budget for a run that never began (SC-4244).
// Ownership follows the work: a launch that actually starts an agent claims the
// ticket for this machine, so "who holds this right now" is answerable from the
// ticket alone (SC-3345). It rides here rather than in each caller for the same
// reason the no-op contract does — a new launch path inherits it without
// rediscovering the rule. A benign single-flight refusal claims nothing: an
// agent is already on it, so the existing claim is the accurate one.
func (d BoardTransitionDeps) launchAgent(ctx context.Context, pmKey, name, prompt string) (launched bool, err error) {
	// Registered BEFORE the launch: the run can fire its first hook event the
	// moment the container starts, and an id minted afterwards would arrive too
	// late to recognise it.
	_, stage, _ := parseAgentName(name)
	runID := d.Runs.Register(name, pmKey, stage)
	if err := d.Launcher.Launch(ctx, name, prompt, d.WorkspaceDir, d.ConfigDir, runID); err != nil {
		// Nothing will ever arrive for a run that did not start, and a single-flight
		// refusal means another launch owns the work — either way the id is dead.
		d.Runs.Forget(runID)
		if stderrors.Is(err, ErrAgentAlreadyRunning) {
			return false, nil
		}
		return false, err
	}
	d.setTicketOwner(pmKey)
	return true, nil
}

// setTicketOwner makes this machine's identity the ticket's owner. Best-effort by
// contract: ownership is a record of who is working, never a precondition for
// working, so a tracker that refuses the claim leaves the stage running and the
// reason in the log rather than failing a launch that already succeeded.
func (d BoardTransitionDeps) setTicketOwner(pmKey string) {
	if d.SetTicketOwner == nil || pmKey == "" {
		return
	}
	if err := d.SetTicketOwner(pmKey); err != nil {
		d.Logger.Debug().Err(err).Str("pm", pmKey).
			Msg("could not claim ticket ownership for this machine; the stage runs regardless")
	}
}

// requiresPlan declares that this launch executes a pre-written plan, so the
// stage must not start on a ticket that has none (SC-2596). It is true for every
// route that carries out a plan (the forward drag into implementation, the
// rework and build retries, an implementation-stage option relaunch) and false
// for the self-contained fix pipelines (autofix, security-fix), which produce
// their plan within the run.
func (d BoardTransitionDeps) startAgentStage(ctx context.Context, pmKey string, stage BoardStage, startedHeader, prompt string, cause WaitCause, requiresPlan bool) (launched bool, err error) {
	// Launch gate: a daemon whose host fails a launch-critical doctor check
	// (docker, agent-skills, claude-auth) cannot serve this stage. Refuse before
	// the claim so NO [human:claim] is posted — the work is left unclaimed for a
	// healthy daemon and the failure surfaces only on this host, never as a ticket
	// marker (SC-912). Returning nil is a silent skip-and-leave, not an error.
	if d.LaunchGate != nil {
		if blockers := d.LaunchGate(ctx); len(blockers) > 0 {
			d.Logger.Warn().
				Str("pm", pmKey).Str("stage", string(stage)).Str("check", blockers[0].ID).
				Msg("board stage launch skipped: launch-critical doctor check failing; leaving work for a healthy daemon")
			return false, nil
		}
	}
	// Dependency gate: work someone deliberately sequenced behind another
	// ticket does not start while that ticket is open. Like the launch gate it
	// refuses before the claim, so nothing is claimed and the card stays
	// cleanly unstarted — but unlike it, the refusal is reported to the caller:
	// no other daemon can serve this stage either, so a silent skip would be a
	// card that never starts for a reason nobody can see.
	if err := d.refuseIfBlocked(ctx, pmKey, stage); err != nil {
		return false, err
	}
	// Plan gate: the implementation stage carries out a plan, so it must not
	// start on a ticket that has none. Checked HERE, at the one chokepoint every
	// launch route funnels through, rather than on the drag gesture alone — the
	// gap that let a non-drag route launch six doomed runs (SC-2596). Like the
	// dependency gate it refuses before the claim, so nothing is claimed; unlike
	// it, the refusal records a [human:needs-planning] marker so the card surfaces
	// the determination back in Planning instead of leaving it invisible.
	if refused, err := d.refuseIfUnplanned(ctx, pmKey, stage, requiresPlan); refused || err != nil {
		return false, err
	}
	// Claim before start: with several daemons on one board, arbitrate who
	// launches this stage so the work is picked up exactly once (SC-660 rule 2).
	// A loser backs off silently — not an error — leaving the started marker and
	// the launch to the winning daemon.
	won, err := d.winClaim(ctx, pmKey, stage)
	if err != nil {
		return false, err
	}
	if !won {
		return false, nil
	}
	// Snapshot the thread BEFORE the launch: this is the last instant the previous
	// stage's done marker is the newest done-state marker, i.e. the eligibility
	// anchor. It is RECORDED only after an agent actually started, so a refused
	// launch leaves no trace of a stage that never began (SC-4244/SC-2462).
	// Best-effort and threshold-gated, so a promptly-chained stage posts nothing.
	waitComments, waitErr := d.Commenter.ListComments(ctx, pmKey)

	name := agentNameFor(pmKey, stage)
	started, err := d.launchAgent(ctx, pmKey, name, prompt)
	if err != nil {
		_ = postMarker(ctx, d.Commenter, pmKey, failureMarker(failedTypeFor(stage), errors.CauseChain(err)))
		return false, errors.WrapWithDetails(err, "launching agent", "pm", pmKey, "stage", string(stage))
	}
	if !started {
		// A benign single-flight refusal: an agent is already working this stage on
		// this machine, so ITS claim, ITS started marker and ITS clock are the
		// accurate record. Post nothing — a started marker here would re-date
		// StageEnteredAt and buy the running agent another StuckRunningGrace from
		// the pass meant to reach it — and report that nothing started, so the
		// retry accounting charges no attempt (SC-4244, SC-2989).
		d.Logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board stage launch refused: an agent is already running this stage on this machine; leaving its record standing")
		return false, nil
	}
	if waitErr == nil {
		recordStageWait(ctx, d.Commenter, pmKey, stage, waitComments, cause, d.DaemonID, d.Logger)
	}
	if _, err := d.Commenter.AddComment(ctx, pmKey, startedHeader); err != nil {
		// The agent IS running: reporting launched=false here would re-create the
		// bug with the sign flipped. The error still surfaces, and the charged
		// attempt correctly stays charged for a launch that happened.
		return true, errors.WrapWithDetails(err, "posting started marker", "pm", pmKey, "stage", string(stage))
	}
	return true, nil
}

// needsPlanningReason is the human-readable line the [human:needs-planning]
// marker carries, so the refused card reads as an instruction, not an error.
const needsPlanningReason = "implementation cannot start: this ticket has no plan. Run planning first, then move it to implementation."

// PlanRedriveBound caps how many times a refused implementation launch may
// drive the card back into planning before giving up and escalating to a
// person (SC-2990). The bound is anchored in the ticket thread itself — via
// countPlanRefusals, which counts the ordinary [human:needs-planning] markers
// already posted — rather than in daemon state, mirroring the SC-2851 outage
// bound: the thread is the one record that survives a daemon restart, a
// handover to a peer daemon, and the state db being wiped. A package var so
// tests can shorten it.
var PlanRedriveBound = 3

// planStuckLegacySentinel is FROZEN. It is not the escalation's wording — it is
// the wording escalations posted before marker.EscalationField existed happen to
// have, and the bound is recounted from comments already on the ticket, so this
// string is how those threads keep deriving as they always did. Nothing writes
// it; editing it silently reclassifies history, which is the defect SC-4245
// removed. New escalations are recognised by the field (isPlanStuck).
const planStuckLegacySentinel = "this ticket could not be planned automatically"

// planStuckHeadline is the escalation's opening sentence — prose, for the person
// who has to plan the ticket by hand. Free to reword: nothing classifies on it.
// It currently reads the same as planStuckLegacySentinel so a peer daemon on an
// older build still recognises a freshly posted escalation; that is a rollout
// convenience, not a coupling, and the two constants are independent.
const planStuckHeadline = "this ticket could not be planned automatically"

// isPlanStuck reports whether a [human:needs-planning] comment is the plan-stuck
// escalation rather than an ordinary refusal. It reads the determination from
// the marker's own field; the substring test is the legacy path for threads
// written before the field existed.
func isPlanStuck(body string) bool {
	if m, ok := marker.ParseBody(body); ok {
		if strings.TrimSpace(m.Fields[marker.EscalationField]) == marker.EscalationPlanStuck {
			return true
		}
		if _, hasField := m.Fields[marker.EscalationField]; hasField {
			return false
		}
	}
	return strings.Contains(body, planStuckLegacySentinel)
}

// countPlanRefusals counts the ordinary (non-stuck) [human:needs-planning]
// markers on the thread — one per automated drive back into planning — the
// value PlanRedriveBound is checked against.
func countPlanRefusals(comments []tracker.Comment) int {
	n := 0
	for _, c := range comments {
		trimmed := strings.TrimSpace(c.Body)
		if strings.HasPrefix(trimmed, NeedsPlanningHeader) && !isPlanStuck(trimmed) {
			n++
		}
	}
	return n
}

// oldestNeedsPlanning returns the earliest [human:needs-planning] marker on
// the thread (ordinary or stuck) — the stuck-since anchor the escalation body
// names, so a person reads "stuck since X", not merely a bare attempt count.
func oldestNeedsPlanning(comments []tracker.Comment) (tracker.Comment, bool) {
	var oldest tracker.Comment
	found := false
	for _, c := range comments {
		if !strings.HasPrefix(strings.TrimSpace(c.Body), NeedsPlanningHeader) {
			continue
		}
		if !found || commentNewer(oldest, c) {
			oldest = c
			found = true
		}
	}
	return oldest, found
}

// planStuckReason renders the escalation's prose: what was tried and since
// when, so the person reading it knows this is not a fresh refusal but a
// ping-pong the bound has already stopped.
func planStuckReason(drives int, since tracker.Comment) string {
	stuckSince := "an unknown time"
	if !since.Created.IsZero() {
		stuckSince = since.Created.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"%s — tried %d time(s), stuck since %s. A person needs to plan this ticket by hand.",
		planStuckHeadline, drives, stuckSince)
}

// planStuckBody composes the escalation marker: the determination in a field,
// the explanation in prose. One composer so writer and reader cannot drift, and
// so the marker-contract test can put exactly what the daemon posts back through
// the protocol's own validator.
func planStuckBody(drives int, since tracker.Comment) string {
	m := failureMarker(MarkerNeedsPlanning, planStuckReason(drives, since))
	m.Fields[marker.EscalationField] = marker.EscalationPlanStuck
	return markerBody(m, marker.EscalationField, "reason")
}

// refuseIfUnplanned refuses an implementation launch on a ticket that carries no
// plan and reports whether it did. The implementation stage exists to carry out
// a plan; without one it can only claim the ticket, run preparation, and discover
// there is nothing to execute — the doomed-launch loop this guards (SC-2596).
//
// It applies only to plan-executing implementation launches: requiresPlan is
// false for the self-contained fix pipelines, which produce their plan within
// the run, and the gate is a no-op for every other stage.
//
// Past the refusal itself (unchanged — implementation still never starts on a
// ticket with no plan) the card is bounded, thread-anchored driven back into
// planning rather than left to park silently, until either a plan lands or the
// ping-pong bound (PlanRedriveBound) is spent, at which point a standing
// plan-stuck [human:needs-planning] escalation reaches a person exactly once
// (SC-2990). The dedup guard is keyed on the newest PLANNING-STAGE marker
// (latestStateInStage), not the newest marker overall — newestTerminalDetermination
// is deliberately NOT reused here: its "newest overall" semantics are
// load-bearing in DeriveBoardCard's terminal promotion, but as this gate's
// dedup key it let a later *-failed marker on another stage defeat the guard
// and re-post the refusal once per attempt.
//
// A comment-read failure is deliberately NOT treated as an absence: a tracker
// blip must not refuse a launch, so the run proceeds and the agent's own plan
// check (which distinguishes a genuine absence from an unreachable tracker)
// remains the backstop.
func (d BoardTransitionDeps) refuseIfUnplanned(ctx context.Context, pmKey string, stage BoardStage, requiresPlan bool) (refused bool, err error) {
	if !requiresPlan || stage != BoardImplementation {
		return false, nil
	}
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		d.Logger.Warn().Err(err).Str("pm", pmKey).
			Msg("board stage: cannot read comments to check for a plan, starting anyway")
		return false, nil
	}
	if hasPlanEvidence(comments) {
		return false, nil
	}
	planState, planMarker := latestStateInStage(comments, BoardPlanning)
	newestIsRefusal := strings.HasPrefix(strings.TrimSpace(planMarker.Body), NeedsPlanningHeader)

	// A plan-stuck escalation already stands: it was said once, to a person,
	// and driving planning again would only repeat the failure that exhausted
	// the bound — say nothing further.
	if newestIsRefusal && isPlanStuck(planMarker.Body) {
		d.Logger.Info().Str("pm", pmKey).
			Msg("board stage: implementation refused — plan-stuck escalation already standing")
		return true, nil
	}
	// Planning is already running from an earlier drive: withhold implementation
	// without re-posting the refusal or launching a second planner.
	if planState == BoardRunning {
		d.Logger.Info().Str("pm", pmKey).
			Msg("board stage: implementation refused — planning is already running")
		return true, nil
	}
	// The ping-pong bound is spent: another drive would only repeat the same
	// failure. Escalate to a person instead, naming what was tried and since
	// when, rather than looping forever between planning and implementation.
	if drives := countPlanRefusals(comments); drives >= PlanRedriveBound {
		since, _ := oldestNeedsPlanning(comments)
		if _, err := d.Commenter.AddComment(ctx, pmKey, planStuckBody(drives, since)); err != nil {
			return true, errors.WrapWithDetails(err, "posting plan-stuck escalation marker", "pm", pmKey)
		}
		d.Logger.Info().Str("pm", pmKey).
			Msg("board stage: implementation refused — plan re-drive bound exhausted; escalated to a person")
		return true, nil
	}
	// Ordinary refusal: surface it only when it is not already the ticket's
	// current planning-stage determination, so a reconcile re-drive does not
	// spam the thread — then drive the card into planning so a person never
	// has to notice the refusal by hand.
	if !newestIsRefusal {
		body := markerBody(failureMarker(MarkerNeedsPlanning, needsPlanningReason))
		if _, err := d.Commenter.AddComment(ctx, pmKey, body); err != nil {
			return true, errors.WrapWithDetails(err, "posting needs-planning marker", "pm", pmKey)
		}
	}
	d.Logger.Info().Str("pm", pmKey).
		Msg("board stage: implementation refused — ticket has no plan; driving it into planning")
	if _, err := d.startAgentStage(ctx, pmKey, BoardPlanning, PlanningStartedHeader, planPrompt(pmKey), WaitCauseRetry, false); err != nil {
		return true, err
	}
	return true, nil
}

// startDeploy launches the deploy pipeline in the background. A package var so
// tests can run the pipeline synchronously.
var startDeploy = func(d BoardTransitionDeps, req BoardTransitionRequest, card BoardCard) {
	go d.deploy(context.Background(), req, card)
}

// runDoneStage starts the pre-merge PR review→fix loop: it opens the branch's
// PR in draft (unmergeable) state, launches the machine reviewer on it, and the
// loop drives reviewer→fixer to convergence before the existing deploy engine
// un-drafts and merges the PR. The reviewer's CI gate can take many minutes, so
// the transition request returns as soon as the loop's first marker is posted
// and the loop reports its progress via markers. The empty-branch guard is
// unchanged — a handoff with no branch has nothing to open.
func (d BoardTransitionDeps) runDoneStage(_ context.Context, req BoardTransitionRequest, card BoardCard) error {
	if card.Branch == "" {
		_ = postMarker(context.Background(), d.Commenter, req.PMKey, marker.Marker{
			Type:   MarkerDeployFailed,
			Fields: fields("reason", "no branch recorded on ready-for-review handoff"),
		})
		return errors.WithDetails("no branch recorded for deploy", "pm", req.PMKey)
	}
	startPRReview(d, req, card)
	return nil
}

// startPRReview opens the draft PR and launches the reviewer in the background.
// A package var so tests can run the loop's first phase synchronously, mirroring
// startDeploy.
var startPRReview = func(d BoardTransitionDeps, req BoardTransitionRequest, card BoardCard) {
	go func() { _ = d.openDraftPRAndReview(context.Background(), req.PMKey, card) }()
}

// openDraftPRAndReview opens the branch's PR in draft (unmergeable) state and
// launches the machine reviewer on it, starting the pre-merge review→fix loop.
// The draft state is a hard guard independent of the daemon: a half-reviewed
// change cannot merge. On the already-merged carve-out (a re-run on shipped
// work) it short-circuits to the terminal success path exactly like DeployBranch.
func (d BoardTransitionDeps) openDraftPRAndReview(ctx context.Context, pmKey string, card BoardCard) error {
	if d.Deployer.BranchMerged(ctx, d.WorkspaceDir, card.Branch) {
		_ = postMarker(ctx, d.Commenter, pmKey, marker.Marker{
			Type:   MarkerDeployed,
			Fields: fields("merged", "already in the base branch; no new PR opened"),
		})
		d.closeTicketBestEffort(pmKey)
		return nil
	}
	res, err := d.Deployer.PushAndCreatePR(ctx, PRRequest{
		WorkspaceDir: d.WorkspaceDir,
		Branch:       card.Branch,
		Title:        pmKey, // adopted on the approval path; title only used on fresh create
		Body:         doneBody(pmKey, card),
		Draft:        true,
	})
	if err != nil {
		if reason, ok := secretStoreFailureHeadline(err); ok {
			return d.deployFailed(pmKey, "", deployReason(reason, err))
		}
		return d.deployFailed(pmKey, "", deployReason(
			"could not push "+card.Branch+" and open its draft pull request — check the branch and forge access, then re-run Deploy", err))
	}
	_, err = d.launchPRLoopAgent(ctx, pmKey, prReviewAgentStage,
		prReviewDispatch(pmKey, res.Number, card.Branch),
		prReviewStartedBody(res.URL, res.Number, card.Branch))
	return err
}

// prReviewStartedBody carries the loop's PR binding on the started marker so the
// Stop-hook driver can recover (url, number, branch) without a forge lookup.
func prReviewStartedBody(url string, number int, branch string) string {
	return markerBody(marker.Marker{
		Type:   MarkerPRReviewStarted,
		Fields: fields("pr", url, "number", strconv.Itoa(number), "branch", branch),
	}, "pr", "number", "branch")
}

func prReviewDispatch(pmKey string, number int, branch string) string {
	return "/human-pr-review " + pmKey + " --pr=" + strconv.Itoa(number) + " --branch=" + branch
}

func prFixDispatch(pmKey string, number int, branch string) string {
	return "/human-pr-fix " + pmKey + " --pr=" + strconv.Itoa(number) + " --branch=" + branch
}

// DefaultDeployFixRounds bounds the automated deploy-fix loop: at most this many
// dispatched fixer rounds before a still-failing deploy reds for a human. Mirrors
// DefaultStageRetries — a mechanical rebase/CI failure is almost always fixed on
// the first pass; a failure that survives two fixer rounds is genuinely stuck.
const DefaultDeployFixRounds = 2

// deployFixDispatch is the deploy-fixer's slash-skill dispatch (sibling of prFixDispatch).
func deployFixDispatch(pmKey string, number int, branch string) string {
	return "/human-deploy-fix " + pmKey + " --pr=" + strconv.Itoa(number) + " --branch=" + branch
}

// deployFixRounds counts dispatched deploy-fix rounds — one per deploy-fix-started
// marker — the value the budget bounds against DefaultDeployFixRounds. Mirrors
// prReviewRounds; per-ticket-lifetime by design (see plan AD2).
func deployFixRounds(comments []tracker.Comment) int {
	n := 0
	for _, c := range comments {
		if strings.HasPrefix(strings.TrimSpace(c.Body), DeployFixStartedHeader) {
			n++
		}
	}
	return n
}

// launchPRLoopAgent launches one loop step's agent (fire-and-forget, no claim:
// the loop is driven by the launching daemon's local Stop events) and records
// the step on the ticket ONLY when an agent actually started. The started body
// is passed in rather than posted by the caller because the order is the point:
// a marker written ahead of a refused launch is a loop step the thread claims
// and nothing performed (SC-4244). A launch failure escalates the card —
// leaving it spinning would strand the loop.
func (d BoardTransitionDeps) launchPRLoopAgent(ctx context.Context, pmKey string, stage BoardStage, prompt, startedBody string) (launched bool, err error) {
	name := agentNameFor(pmKey, stage)
	started, err := d.launchAgent(ctx, pmKey, name, prompt)
	if err != nil {
		body := markerBody(failureMarker(MarkerPRReviewFailed, "could not launch the PR "+string(stage)+" agent — "+errors.CauseChain(err)))
		_, _ = d.Commenter.AddComment(ctx, pmKey, body)
		return false, errors.WrapWithDetails(err, "launching PR loop agent", "pm", pmKey, "stage", string(stage))
	}
	if !started {
		// An agent on this machine already owns this loop step: its marker stands,
		// the loop's own clock is not re-dated, and the running step's Stop event
		// will drive the next action (SC-4244, SC-2603).
		d.Logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
			Msg("board PR loop: launch refused, an agent already owns this step; leaving its record standing")
		return false, nil
	}
	if _, err := d.Commenter.AddComment(ctx, pmKey, startedBody); err != nil {
		return true, errors.WrapWithDetails(err, "posting PR loop started marker", "pm", pmKey, "stage", string(stage))
	}
	return true, nil
}

// prLoopNumber recovers the loop's PR number from the latest pr-review-started
// marker's binding; 0 when absent or unparseable.
func prLoopNumber(comments []tracker.Comment) int {
	n, _ := strconv.Atoi(strings.TrimSpace(latestPrefixedLine(comments, PRReviewStartedHeader, "number:")))
	return n
}

// prLoopURL recovers the loop's PR URL from the latest pr-review-started marker.
func prLoopURL(comments []tracker.Comment) string {
	return latestPrefixedLine(comments, PRReviewStartedHeader, "pr:")
}

// AdvancePRLoop is the deploy-stage loop executor. On each reviewer/fixer exit
// the failure watcher calls it with the outcome the step recorded in the state
// store (reviewVerdict or fixExit); it reads the loop's markers, asks the pure
// decider for the next action, and executes it: launch the reviewer, launch the
// fixer, un-draft + merge via the existing DeployBranch, or red the card for a
// human. Human PR review runs out of band and never enters here.
func (d BoardTransitionDeps) AdvancePRLoop(ctx context.Context, pmKey string, outcome PRLoopOutcome) error {
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading comments for PR loop", "pm", pmKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	number, url, branch := prLoopNumber(comments), prLoopURL(comments), card.Branch
	switch EvaluatePRLoop(comments, outcome) {
	case PRActionReview:
		_, err := d.launchPRLoopAgent(ctx, pmKey, prReviewAgentStage,
			prReviewDispatch(pmKey, number, branch), prReviewStartedBody(url, number, branch))
		return err
	case PRActionFix:
		_, err := d.launchPRLoopAgent(ctx, pmKey, prFixAgentStage,
			prFixDispatch(pmKey, number, branch), PRFixStartedHeader)
		return err
	case PRActionMerge:
		// Record the loop converging BEFORE acting on it. Both launches and the
		// escalation already post a marker, so without this the one outcome the
		// thread never recorded was success — and it is the outcome a reader most
		// needs, because it is what separates "the review passed, the merge is
		// running" from "the review is still going". Posting it also retires the
		// loop sub-phase, so the badge stops saying "PR review…" for the whole of
		// the CI gate, rebase and merge that follow. A failure to post is not
		// fatal: the merge is the work, and refusing to ship over a missing
		// comment would trade a lost sentence for lost code.
		if _, err := d.Commenter.AddComment(ctx, pmKey, PRReviewPassedHeader); err != nil {
			d.Logger.Warn().Err(err).Str("pm", pmKey).
				Msg("board PR loop: could not record the passing review; continuing to the merge")
		}
		if err := d.Deployer.MarkReadyForReview(ctx, d.WorkspaceDir, number); err != nil {
			return d.deployFailed(pmKey, url, deployReason(
				"the reviewed PR could not be marked ready for merge — open the PR and mark it ready, then re-run Deploy", err))
		}
		// Reuse the untouched deploy engine: it adopts the now-ready open PR
		// (forge.AdoptOrCreatePullRequest), runs the CI gate, freshness rebase and merge.
		return d.DeployBranch(ctx, pmKey, pmKey, doneBody(pmKey, card), branch)
	default: // PRActionEscalate
		return d.escalatePRLoop(ctx, pmKey, comments, outcome)
	}
}

// escalatePRLoop routes a non-converging loop to the right surface, and what
// decides the surface is what the fixer actually named:
//
//   - two or more directions — a genuine fork. It becomes a [human:options]
//     block naming the implementation stage, so choosing rebuilds through the
//     normal chain and re-adopts the still-open draft PR.
//   - exactly one direction — not a fork. The daemon pursues it (SC-3630).
//   - none — not a decision either. The fixer stopped without naming a way
//     forward, which is a failure with a reason, and reds the done stage.
//
// Everything else — a spent round budget, an unreviewable PR, an outcome the
// daemon cannot classify — reds the done stage as before.
//
// The card used to ask in all three cases, filling an empty block with invented
// answers so it stayed well-formed. Nothing about the real problem survived
// that, and the user was left clicking a fabricated button to re-run a loop
// with no new information.
//
// Idempotent: a durable re-drive must never re-post the block, so an already-open
// options block short-circuits.
func (d BoardTransitionDeps) escalatePRLoop(ctx context.Context, pmKey string, comments []tracker.Comment, outcome PRLoopOutcome) error {
	if _, open := openOptionsBlock(comments); open {
		return nil
	}
	// Already escalated and nothing has moved since: say it once. A single run can
	// produce two events that both look like its exit — the hook fires StopFailure
	// on an API error and Stop when the turn ends, and the parser's own contract
	// is that a Stop may follow a StopFailure — which drove this twice sixteen
	// seconds apart on SC-3613 and posted the identical marker both times.
	if _, latest := latestStateInStage(comments, BoardDoneStage); strings.HasPrefix(strings.TrimSpace(latest.Body), PRReviewFailedHeader) {
		return nil
	}
	stage := latestPRLoopStage(comments)
	if stage == PRStageFix && outcome.FixExit != PRFixDone {
		switch opts := outcome.FixOptions; {
		case len(opts) >= marker.MinDecisionOptions:
			m, order := optionsMarker(BoardImplementation, decisionContext(outcome), opts)
			return postMarker(ctx, d.Commenter, pmKey, m, order...)
		case len(opts) == 1 && prReviewRounds(comments) < DefaultPRReviewRounds:
			return d.pursueSoleDirection(ctx, pmKey, comments, opts[0])
		}
		// No directions — or one the round budget can no longer afford to
		// pursue. Both fall through to the failed marker, which says what the
		// fixer reported and offers the retry the card already knows how to run.
	}
	_, _ = d.Commenter.AddComment(ctx, pmKey,
		markerBody(failureMarker(MarkerPRReviewFailed, prEscalationReason(stage, outcome, d.Diagnose))))
	return nil
}

// decisionContext is the line the block leads with: what the fixer said it was
// stuck on. It has a generic fallback only because a fork with real answers and
// no summary is still answerable — unlike an empty block, whose generic context
// described nothing at all.
func decisionContext(outcome PRLoopOutcome) string {
	if outcome.FixSummary != "" {
		return outcome.FixSummary
	}
	return "the PR fixer stopped on a decision and named the directions below"
}

// pursueSoleDirection takes the only answer on offer instead of asking for it.
//
// A choice between one thing is not a choice — it is a continue button dressed
// as a fork, and it stops the board on something no human judgment can improve.
// The protocol already refuses to POST such a block (marker.MinDecisionOptions);
// this is the other half, so the loop does not merely avoid writing a dead end
// but actually moves.
//
// It records the choice exactly as a human's click records it — same
// [human:option-chosen] marker, same relaunch — so the trail reads the same
// whoever made the call, and the card is never resumed without a record.
//
// The one thing it will not take from the fixer is a WAIT. Putting this ticket
// behind another one is a sequencing judgement about a backlog, which is the
// class of call the daemon may never make for itself; taken here it would also
// hold the card on a decision no person ever saw. Dropped rather than refused —
// the direction itself is still worth pursuing, and this way the loop moves.
func (d BoardTransitionDeps) pursueSoleDirection(ctx context.Context, pmKey string, comments []tracker.Comment, only BoardOption) error {
	only.WaitsFor = ""
	return d.pursueDecision(ctx, pmKey, comments, BoardImplementation, only)
}

// prEscalationReason renders the actionable headline the failed marker's badge
// shows.
//
// The unrecorded case is kept distinct from every recorded one. A step that
// wrote nothing did not decide anything — it died, or never got far enough to
// report — and saying "unreadable outcome" for it sent a human to read a review
// that was never written. Where a diagnosis of the dead run is available it
// replaces the generic line entirely, the same way an ordinary stage failure
// reports its cause (SC-1688); without one — notably the durable reconcile
// re-drive, which has no agent in hand — the line at least names what was
// missing instead of implying something unparseable was found.
func prEscalationReason(stage PRLoopStage, outcome PRLoopOutcome, diagnose BoardFailureDiagnoser) string {
	switch {
	case outcome.stepStale(stage):
		return staleStepReason(stage)
	case stage == PRStageFix && outcome.FixExit == PRFixDone && outcome.headStalled():
		return "the PR fixer recorded done but added no commit — the reviewed head is unchanged, so another review would loop; check the fixer's log and the PR, then re-run Deploy"
	case outcome.FixExit == string(ExitNeedsInput):
		return needsInputReason(outcome)
	case outcome.ReviewVerdict == PRVerdictChanges:
		return "the machine review did not converge within the round budget — review the PR yourself, then re-run Deploy"
	case outcome.ReviewVerdict == PRVerdictUnreviewable:
		return "the PR could not be reviewed (bad binding or empty diff) — check the PR, then re-run Deploy"
	case !outcome.stepRecorded(stage):
		return unrecordedStepReason(stage, outcome, diagnose)
	default:
		return "the PR review→fix loop stopped on an outcome it could not classify — check the PR and its review, then re-run Deploy"
	}
}

// needsInputReason explains a fixer that stopped for a decision without naming
// one (SC-3630). It reaches the failed marker only when the fixer listed no
// directions — with directions the loop asks instead, and with exactly one it
// pursues it — so the card's job here is to say what the fixer reported, not to
// invent a question out of the fact that it reported nothing.
//
// The fixer's own summary leads when it wrote one. Without it the line says the
// step ended without naming what it was stuck on, which is the honest reading:
// sending a human to "read the review comments and decide" implies a question
// was recorded there, and none was.
func needsInputReason(outcome PRLoopOutcome) string {
	if s := strings.TrimSpace(outcome.FixSummary); s != "" {
		return "the PR fixer stopped for a decision it could not make: " + s + " — decide, then re-run Deploy"
	}
	return "the PR fixer stopped for a decision but named neither the question nor a way forward — read the PR review comments and the fixer's log, then re-run Deploy"
}

// staleStepReason names the record the loop could not confirm was current
// (SC-2378): a state-store read that raced ahead of the reviewer's or fixer's
// final write, and stayed unconfirmed through its bounded settle backoff. The
// loop escalates rather than risk acting on a superseded verdict or exit —
// this is the operator-facing explanation of which one it was.
func staleStepReason(stage PRLoopStage) string {
	what := "the PR review→fix loop step's outcome"
	switch stage {
	case PRStageReview:
		what = "the review verdict"
	case PRStageFix:
		what = "the fixer's exit"
	}
	return "the loop could not confirm " + what + " was fully written before acting on it — check the PR and its review, then re-run Deploy"
}

// unrecordedStepReason explains a loop step that left no outcome behind. The
// RETURNED escalation line is always the house-style situation+next-action —
// never a diagnoser's raw post-mortem headline/detail (SC-3024): a diagnosis
// carries machine vocabulary (container/OOM/exit-code) a card-facing marker
// must never print as THE message. The diagnosis still reaches the ticket via
// the ordinary stage-failure evidence path when that path runs; this
// escalation only names what is missing and the one gesture that recovers it.
func unrecordedStepReason(stage PRLoopStage, _ PRLoopOutcome, _ BoardFailureDiagnoser) string {
	step, report := "review→fix loop step", "an outcome"
	switch stage {
	case PRStageReview:
		step, report = "PR reviewer", "a verdict"
	case PRStageFix:
		step, report = "PR fixer", "an exit"
	}
	return "the " + step + " stopped before recording " + report +
		" — check the PR and its review, then re-run Deploy"
}

// AdvanceDeployFix is the deploy-fixer's Stop-event driver. On the fixer's exit the
// failure watcher calls it with the exit the agent recorded in stage.deploy-fix. A
// `done` exit publishes the fixer's local resolution and re-runs the deploy pipeline
// (the branch is then ready for a fresh CI gate + merge); any other exit reds the card
// with a terminal deploy-failed. The deployFixRounds budget already bounds how many
// times the pipeline re-enters here, so a genuinely unfixable failure terminates.
func (d BoardTransitionDeps) AdvanceDeployFix(ctx context.Context, pmKey string, fixExit StageExit) error {
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading comments for deploy fix", "pm", pmKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	if fixExit == ExitDone {
		// The fixer resolved the conflict in a container that holds no push
		// credentials, exactly like every other board fixer — so its deliverable is
		// the local branch, and publishing it is the daemon's job. Publishing before
		// the deploy runs is what makes the resolution visible at all: the deploy
		// reads the branch from origin (branchTip prefers the origin ref), so an
		// unpublished resolution would be silently discarded and the same conflict
		// re-hit (SC-2845).
		if _, err := d.Deployer.PublishResolvedBranch(ctx, d.WorkspaceDir, card.Branch); err != nil {
			return d.deployFailed(pmKey, "", deployReason(
				"the deploy fixer's resolution could not be published to "+card.Branch+" — check the branch, then re-run Deploy",
				err))
		}
		return d.DeployBranch(ctx, pmKey, pmKey, doneBody(pmKey, card), card.Branch)
	}
	// SC-3857: the done stage was already declared dead by an earlier escalation
	// with no relaunch since (a dispatch that actually started a fixer posts
	// [human:deploy-fix-started] before the fixer can exit, so a fresh dispatch
	// flips this back to false; a dispatch refused because a fixer is already
	// running posts nothing and deliberately leaves the guard on, because the
	// fixer it would re-tell about is the one still going, SC-4244) — posting
	// again would only re-date the card.
	// deployFailed above is deliberately NOT guarded the same way (AD5): it can
	// fire before any running done-stage marker exists at all, and a guard there
	// would swallow a genuine new failure on a board Deploy re-drop.
	if stageAlreadyFailed(comments, BoardDoneStage) {
		return nil
	}
	_, _ = d.Commenter.AddComment(ctx, pmKey,
		markerBody(failureMarker(MarkerDeployFailed, deployFixEscalationReason(fixExit, dispatchedFailure(comments)))))
	return nil
}

// dispatchedFailure recovers WHAT the deploy fixer was sent to fix: the headline
// the gate wrote onto the newest [human:deploy-fix-started] marker, which for a
// CI failure already names the failing checks ("CI checks failed on the pull
// request (failing: frontend-test)"). The escalation had this on the ticket all
// along and quoted none of it.
func dispatchedFailure(comments []tracker.Comment) string {
	var headline string
	for _, c := range comments {
		// ParseBody, never line position: a signed marker carries machine:/build:
		// between the header and the prose, so "the line after the header" is a
		// signature field rather than the diagnosis.
		m, ok := marker.ParseBody(c.Body)
		if !ok || m.Type != deployFixStartedType {
			continue
		}
		if line, _, _ := strings.Cut(strings.TrimSpace(m.Body), "\n"); line != "" {
			headline = line
		}
	}
	return headline
}

// deployFixStartedType is DeployFixStartedHeader's marker type — the name
// marker.ParseBody reports, without the human: prefix and brackets.
const deployFixStartedType = "deploy-fix-started"

// deployFixEscalationReason renders the actionable headline the failed marker shows
// when the deploy fixer did not converge.
//
// It names the condition that is blocking, and it does not offer a gesture that
// cannot work. "Re-run Deploy" was the only instruction the default case gave,
// and re-running changes nothing about the branch — so the same check fails the
// same way, which is what SC-3615 recorded: a card whose one offered move
// reproduced its own failure, and whose actual cause (a single red check) took
// three queries to establish though it was written on the ticket already.
func deployFixEscalationReason(fixExit StageExit, dispatched string) string {
	blocking := ""
	if dispatched != "" {
		blocking = " The failure it was sent to fix: " + dispatched
	}
	switch fixExit {
	case ExitNeedsInput:
		return "the deploy fixer needs a human decision — read the PR and its CI, decide, then re-run Deploy." + blocking
	case ExitNeedsHumanWork:
		return "the deploy failure needs manual work the fixer could not do — resolve it on the branch, then re-run Deploy." + blocking
	default:
		if dispatched != "" {
			return "the deploy fixer could not recover the deploy. Fix it on the branch and push — " +
				"re-running Deploy alone will hit the same failure." + blocking
		}
		return "the deploy fixer stopped without recovering the deploy — check the PR and its CI, then re-run Deploy"
	}
}

// deployGate queues deploy pipelines: the Deploy button ships every ready fix
// in one click, and concurrent pipelines race each other onto the mainline —
// the first merge moves the base branch and the forge rejects the rest
// ("base branch was modified"), redding cards whose fixes are perfectly fine.
// One deploy at a time, each waiting for the previous one to land, is the
// queue the button implies (SC-296).
var deployGate sync.Mutex

// deploy walks the pipeline to its end. It runs detached from the transition
// request (whose context dies with the connection), bounded by deployTimeout —
// the clock starts when the deploy leaves the queue, so a queued deploy never
// pays for its predecessors' CI waits.
func (d BoardTransitionDeps) deploy(ctx context.Context, req BoardTransitionRequest, card BoardCard) {
	// The board reads the outcome from the posted markers; the returned error
	// exists for CLI callers that need an exit code.
	_ = d.DeployBranch(ctx, req.PMKey, req.PMTitle, doneBody(req.PMKey, card), card.Branch)
}

// settleDraft decides a pull request the machine review loop is still holding,
// BEFORE the CI gate rather than at the merge.
//
// The loop opens its PR draft so a half-reviewed change cannot be merged even if
// the daemon's own gate failed, and only the loop's approval un-drafts it. A
// deploy arriving by any other route — the CLI, the board's Deploy, a deploy-fix
// re-run — used to spend the whole CI wait and then take a forge 405 that named
// nothing about drafts (SC-4027). Deciding it here means the card says what is
// actually holding the change, and costs nothing when there is no draft.
//
// MergeDraftPR is a person overriding the interlock, so the log says the un-draft
// was deliberate rather than leaving a silent release in the trail.
//
// Extracted from DeployBranch rather than inlined: the gate is already at the
// complexity ceiling, and "what to do about a draft" is one subject with its own
// two outcomes.
func (d BoardTransitionDeps) settleDraft(ctx context.Context, pmKey string, res PRResult, logger zerolog.Logger) error {
	if !res.Draft {
		return nil
	}
	if !d.MergeDraftPR {
		return d.deployFailed(pmKey, res.URL, deployReason(
			"this pull request is held in draft by the machine review loop, so the forge will not merge it — it un-drafts itself when the review approves; to ship it without that, re-run the deploy with --ready",
			nil))
	}
	logger.Info().Int("pr", res.Number).Msg("deploy: un-drafting the reviewed PR on explicit instruction")
	if err := d.Deployer.MarkReadyForReview(ctx, d.WorkspaceDir, res.Number); err != nil {
		return d.deployFailed(pmKey, res.URL, deployReason(
			"the pull request could not be marked ready for merge — open the PR and mark it ready, then re-run Deploy", err))
	}
	return nil
}

// regateAfterRebase re-establishes the two facts a freshness rebase invalidated,
// and is a no-op when no rebase happened.
//
// The force-push rewrote the head, which re-triggers CI on it and clears the
// forge's cached mergeability. Merging into either of those is the SC-1184 race:
// GitHub reports the state unstable and 405s a merge on a branch that is
// perfectly clean. So the CI gate runs again on the rebased head — the
// mergeability recompute alone does not cover in-flight checks — and then the
// recompute is waited out.
//
// Extracted from DeployBranch to keep that function inside the complexity gate;
// it is one subject, "the branch moved, so re-check what moving invalidated".
func (d BoardTransitionDeps) regateAfterRebase(ctx context.Context, pmKey string, res PRResult, branch string, rebased bool, logger zerolog.Logger) error {
	if !rebased {
		return nil
	}
	logger.Info().Int("pr", res.Number).Msg("deploy: branch was stale; rebased onto the base, re-gating CI")
	if err := d.waitForChecks(ctx, res); err != nil {
		if ciFailureFixable(err) {
			return d.deployFailedOrDispatchFixer(ctx, pmKey, res, ciFailureHeadline(err), err, branch)
		}
		return d.deployFailed(pmKey, res.URL, deployReason(ciFailureHeadline(err), err))
	}
	if err := d.awaitMergeable(ctx, res.Number); err != nil {
		headline := "the forge still reports the pull request unmergeable after the freshness rebase — open the PR to see why, then re-run Deploy"
		if stateUnreadable(err) {
			headline = "could not read the pull request's mergeability — " + credentialRemedy
		}
		return d.deployFailed(pmKey, res.URL, deployReason(headline, err))
	}
	return nil
}

// DeployBranch runs the deterministic deploy gate for pmKey's branch: the
// already-merged short-circuit, push + PR, the CI gate, the freshness rebase,
// the merge, branch cleanup, markers, and the ticket close. Failures are both
// posted as deploy-failed markers (the board's channel) and returned (the CLI's
// channel).
func (d BoardTransitionDeps) DeployBranch(ctx context.Context, pmKey, title, prBody, branch string) error {
	// The queue is part of the story: a deploy that waited behind another one
	// has not started yet, and only the log can say which of the two a stalled
	// operator is looking at.
	logger := d.Logger.With().Str("pm", pmKey).Str("branch", branch).Logger()
	// Announce the run to the stuck-running sweep. The ticket's marker clock
	// started before this call and cannot see the queue, so a deploy waiting its
	// turn read as one that had died (SC-4150); the registry is the only place
	// the engine's own progress is knowable, since a deploy runs in-process and
	// registers no agent the sweep could list.
	deployRunQueued(pmKey, time.Now())
	defer deployRunFinished(pmKey)
	logger.Info().Msg("deploy: queued")
	deployGate.Lock()
	defer deployGate.Unlock()
	deployRunDequeued(pmKey, time.Now())
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()
	logger.Info().Dur("timeout", deployTimeout).Msg("deploy: started")

	// Already-merged carve-out: a re-run Deploy on a card whose branch is already
	// on the base has nothing to ship. Opening a PR would draw the forge's 422
	// "No commits between" and red a card that is genuinely finished — so
	// short-circuit to the terminal success path (deployed/done, ticket closed).
	// This mirrors the "already done, stop cleanly" carve-outs Planning and
	// Implementation already carry (SC-911).
	if d.Deployer.BranchMerged(ctx, d.WorkspaceDir, branch) {
		logger.Info().Msg("deploy: branch is already on the base; nothing to ship")
		_, _ = d.Commenter.AddComment(ctx, pmKey,
			markerBody(marker.Marker{
				Type:   MarkerDeployed,
				Fields: fields("merged", "already in the base branch; no new PR opened"),
			}))
		d.closeTicketBestEffort(pmKey)
		return nil
	}

	res, err := d.Deployer.PushAndCreatePR(ctx, PRRequest{
		WorkspaceDir: d.WorkspaceDir,
		Branch:       branch,
		Title:        title,
		Body:         prBody,
	})
	if err != nil {
		if reason, ok := secretStoreFailureHeadline(err); ok {
			return d.deployFailed(pmKey, "", deployReason(reason, err))
		}
		return d.deployFailed(pmKey, "", deployReason(
			"could not push "+branch+" and open its pull request — check the branch and forge access, then re-run Deploy",
			err))
	}
	logger.Info().Int("pr", res.Number).Str("url", res.URL).Msg("deploy: pull request open")
	if err := d.settleDraft(ctx, pmKey, res, logger); err != nil {
		return err
	}
	if err := d.waitForChecks(ctx, res); err != nil {
		if ciFailureFixable(err) {
			return d.deployFailedOrDispatchFixer(ctx, pmKey, res, ciFailureHeadline(err), err, branch)
		}
		return d.deployFailed(pmKey, res.URL, deployReason(ciFailureHeadline(err), err))
	}
	// Freshness stage: own the branch's mergeability BEFORE attempting the merge.
	// When main has advanced past the branch point the forge would reject the
	// merge (GitHub 405) and the card would dead-end; rebasing and re-pushing here
	// turns that terminal failure into a mechanical, human-free recovery. A real
	// conflict surfaces as a loud deploy-failed instead of a blind merge attempt.
	rebased, ensureErr := d.Deployer.EnsureMergeable(ctx, PRRequest{
		WorkspaceDir: d.WorkspaceDir,
		Branch:       branch,
	})
	if ensureErr != nil {
		// A rebase is strictly stronger than the forge's three-way end-state
		// merge: it can conflict on an intermediate commit the merge never sees.
		// Consult the forge's mergeable verdict and the green CI on the
		// (rebase-aborted, unchanged) tip before redding the card (SC-804).
		proceed, readErr := d.forgeMergeableFallback(ctx, res)
		if readErr != nil {
			// The fallback could not READ the forge's verdict (a credential/vault
			// failure): this is not a conflict, so report the unreadable state with
			// the remedy and never dispatch the fixer on an unknown state (SC-1996).
			return d.deployFailed(pmKey, res.URL, deployReason(
				"could not read the pull request's mergeability — "+credentialRemedy, readErr))
		}
		if !proceed {
			return d.deployFailedOrDispatchFixer(ctx, pmKey, res,
				"the branch conflicts with the base — resolve the conflict on "+branch+" (rebase it onto the base branch), then re-run Deploy",
				ensureErr, branch)
		}
	}
	if err := d.regateAfterRebase(ctx, pmKey, res, branch, rebased, logger); err != nil {
		return err
	}
	if err := d.mergeWithRetry(ctx, res.Number); err != nil {
		return d.deployFailed(pmKey, res.URL, deployReason(
			"the forge refused the merge — open the PR to see why, then re-run Deploy",
			err))
	}
	// Past the merge the work IS shipped: branch cleanup and the ticket close
	// are best-effort and must never turn the card red. Best-effort here means
	// recorded-and-surfaced, not silent: a failed close leaves the card in the
	// board's Fix column (the frontend only drops a card once the ticket leaves
	// the tracker's open list), so the operator must see it and close by hand.
	logger.Info().Int("pr", res.Number).Str("url", res.URL).Msg("deploy: merged")
	_ = d.Deployer.DeleteRemoteBranch(ctx, d.WorkspaceDir, branch)
	_ = postMarker(ctx, d.Commenter, pmKey, marker.Marker{
		Type: MarkerDeployed, Fields: fields("pr", res.URL),
	})
	d.closeTicketBestEffort(pmKey)
	logger.Info().Msg("deploy: done")
	return nil
}

// headlineOf takes the actionable first line of a marker body — the same line
// the card's badge shows — so a log line carries the verdict without the cause
// chain's newlines running through it.
func headlineOf(reason string) string {
	if i := strings.IndexByte(reason, '\n'); i >= 0 {
		return reason[:i]
	}
	return reason
}

// failureReason renders a deploy-failed marker body per the marker-body
// convention: an actionable headline first (the card badge/tooltip shows
// exactly that line — it must tell the user what to do next), then the raw
// cause as the detail block for the detail pane.
func deployReason(headline string, cause error) string {
	if cause == nil {
		return headline
	}
	return headline + "\n\n" + errors.CauseChain(cause)
}

// deployStateUnreadableDetail tags a deploy error whose real cause is that the
// daemon could not READ the state it was gating on (an expired 1Password/vault
// session makes the token unreadable), as opposed to reading a genuine negative
// verdict. It is the structured "UNKNOWN / could-not-determine" category that
// keeps a credential failure from masquerading as a check failure or a conflict
// (SC-1996), classified via errors.AllDetails like isAlreadyExists reads
// statusCode (internal/forge/forge.go).
const deployStateUnreadableDetail = "deployStateUnreadable"

// deployFailingChecksDetail / deployRunningChecksDetail carry the comma-joined
// names of the checks that failed (or were still running at timeout) as a
// structured detail on the gate error, so ciFailureHeadline can name them in the
// next-step headline the fixer reads — read via errors.AllDetails, exactly as
// deployStateUnreadableDetail is.
const (
	deployFailingChecksDetail = "failingChecks"
	deployRunningChecksDetail = "runningChecks"
)

// credentialRemedy is the shared next-step for every unreadable-state headline:
// it names the failure as a credential/vault problem, disowns the two verdicts
// it must never be mistaken for (a check failure, a conflict), and gives the
// concrete remedy so the operator acts on the real cause instead of chasing a
// green PR's phantom failure.
const credentialRemedy = "this is a credential or vault failure (e.g. an expired 1Password/op session), not a check failure; restore access (re-run `op signin`), then re-run Deploy" // #nosec G101 -- remedy text, not a credential

// markStateUnreadable wraps a read error with the UNKNOWN-outcome tag, preserving
// the cause chain (walked by CauseChain into the detail block) while giving a
// machine-readable signal the routing reads to steer away from the CI-failure
// headline and the fixer.
func markStateUnreadable(cause error, message string, details ...any) error {
	return errors.WrapWithDetails(cause, message, append(details, deployStateUnreadableDetail, true)...)
}

// stateUnreadable reports whether an error is the UNKNOWN could-not-determine
// outcome — a state the daemon failed to read rather than a verdict it read.
func stateUnreadable(err error) bool {
	unreadable, _ := errors.AllDetails(err)[deployStateUnreadableDetail].(bool)
	return unreadable
}

// secretStoreFailureHeadline returns an actionable deploy-failed headline for a
// secret-store failure and true when err is one, so a failed secret read is
// never reported as a branch, forge, or CI failure and is never handed to a code
// fixer (SC-2042). Returns ("", false) for any non-secret error.
func secretStoreFailureHeadline(err error) (string, bool) {
	switch {
	case vault.IsAuthFailure(err):
		return "the secret store is not authenticated — sign in on the daemon host (op signin / gh auth login), then re-run Deploy", true
	case vault.IsStoreUnreachable(err):
		return "the secret store is unreachable — check its CLI is installed and reachable on the daemon host, then re-run Deploy", true
	case vault.IsSecretMissing(err):
		return "a configured secret reference could not be found in the store — fix the reference in .humanconfig, then re-run Deploy", true
	case stderrors.Is(err, vault.ErrCauseUndetermined):
		return "reading a configured secret failed — check the secret store on the daemon host, then re-run Deploy", true
	}
	return "", false
}

// ciFailureHeadline maps the CI gate's failure modes to their next step. A
// secret-store failure (SC-2042) and an unreadable state (SC-1996) are both
// credential/read failures the operator fixes outside the branch, never a
// failing check the checks themselves reported — so neither ever claims the
// checks failed.
func ciFailureHeadline(err error) string {
	if reason, ok := secretStoreFailureHeadline(err); ok {
		return reason
	}
	if stateUnreadable(err) {
		return "could not read the pull request's check state — " + credentialRemedy
	}
	if strings.Contains(err.Error(), "timed out") {
		return "CI did not finish within the deploy window" +
			checkSuffix(err, deployRunningChecksDetail, "still running") +
			" — check the PR's checks, then re-run Deploy"
	}
	return "CI checks failed on the pull request" +
		checkSuffix(err, deployFailingChecksDetail, "failing") +
		" — fix the failing checks, then re-run Deploy"
}

// checkSuffix renders " (label: a, b)" from a names detail on err, or "" when no
// names were captured (a best-effort read that came back empty).
func checkSuffix(err error, detailKey, label string) string {
	names, _ := errors.AllDetails(err)[detailKey].(string)
	if names == "" {
		return ""
	}
	return " (" + label + ": " + names + ")"
}

// ciFailureFixable reports whether a CI gate error is a genuine check FAILURE a
// fixer can repair (lint/test), as opposed to a gate timeout, an unreadable
// state, or a secret-store failure. A timeout is an infra/slowness signal, an
// unreadable state and a secret-store failure are both credential failures —
// none of the three has anything for a code fixer to change (SC-1996, SC-2042).
func ciFailureFixable(err error) bool {
	if _, ok := secretStoreFailureHeadline(err); ok {
		return false // a failed secret read is not a code defect
	}
	return err != nil && !strings.Contains(err.Error(), "timed out") && !stateUnreadable(err)
}

// awaitMergeable waits for the forge's asynchronous mergeability recompute to
// settle after a freshness-rebase re-push. Read errors and a false verdict
// both retry — the recompute window routinely yields either — until the
// timeout, which is the point where "still computing" and "genuinely
// unmergeable" can no longer be told apart.
func (d BoardTransitionDeps) awaitMergeable(ctx context.Context, number int) error {
	deadline := time.Now().Add(mergeablePollTimeout)
	for {
		mergeable, err := d.Deployer.PullRequestMergeable(ctx, d.WorkspaceDir, number)
		if err == nil && mergeable {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				// A persistent read error is an unreadable state, not a verdict of
				// unmergeable — tag it UNKNOWN so the card reports a credential
				// failure rather than blaming the branch (SC-1996).
				return markStateUnreadable(err, "could not read the pull request's mergeability", "pr", number)
			}
			return errors.WithDetails("forge reports the pull request unmergeable", "pr", number)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mergeablePollInterval):
		}
	}
}

// mergeWithRetry merges the PR, riding out a transient merge refusal with
// bounded backoff. After a freshness rebase (or a concurrent deploy advancing
// the base) the forge can report the head unstable/behind for a beat and 405
// the merge with a racy "not mergeable" — that clears on its own, so retrying
// lets the deploy self-heal instead of dead-ending the card. A genuine,
// terminal refusal (a real conflict) is not retried: it is returned at once so
// the card reds with a real cause (SC-1184).
func (d BoardTransitionDeps) mergeWithRetry(ctx context.Context, number int) error {
	deadline := time.Now().Add(mergeRetryTimeout)
	for {
		err := d.Deployer.MergePullRequest(ctx, d.WorkspaceDir, number)
		if err == nil || !isTransientMergeRefusal(err) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mergeRetryInterval):
		}
	}
}

// isTransientMergeRefusal reports whether a merge error is the forge's racy
// post-rebase refusal (a 405 "Pull Request is not mergeable" while the head is
// still unstable/behind) rather than a genuine, terminal conflict. Only the
// former is worth retrying — a real conflict never clears on its own (SC-1184).
func isTransientMergeRefusal(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// A draft refusal is also a 405, and it is the opposite of transient: nothing
	// about the branch changes while it is retried, so matching it here spent the
	// full retry window on a refusal that was never going to lift (SC-4027).
	if strings.Contains(msg, "still a draft") {
		return false
	}
	return strings.Contains(msg, "not mergeable") || strings.Contains(msg, "405")
}

// forgeMergeableFallback reports whether the deploy may proceed to the merge
// despite a failed mechanical rebase: proceed is true only when the forge reports
// the PR mergeable AND CI is green on the tip. A read error is returned distinctly
// as readErr (tagged UNKNOWN) rather than folded into proceed=false, so the caller
// can tell "could not determine" from "determined not mergeable" and never blame a
// conflict for a credential failure (SC-804, SC-1996).
func (d BoardTransitionDeps) forgeMergeableFallback(ctx context.Context, res PRResult) (proceed bool, readErr error) {
	mergeable, err := d.Deployer.PullRequestMergeable(ctx, d.WorkspaceDir, res.Number)
	if err != nil {
		return false, markStateUnreadable(err, "could not read the pull request's mergeability", "pr", res.Number)
	}
	if !mergeable {
		return false, nil
	}
	state, err := d.Deployer.PullRequestChecks(ctx, d.WorkspaceDir, res.Number)
	if err != nil {
		return false, markStateUnreadable(err, "could not read the pull request's check state", "pr", res.URL)
	}
	return state == forge.ChecksPassing, nil
}

// closeTicketBestEffort runs the automated post-merge close. It never fails the
// deploy: on error it retries once (most close failures are transient tracker
// blips), then — if still failing — logs at warn and posts a [human:close-failed]
// marker so the shipped-but-open card is flagged for manual close. The marker is
// deliberately non-stage (see CloseFailedHeader), so the card stays green.
func (d BoardTransitionDeps) closeTicketBestEffort(pmKey string) {
	if d.CloseTicket == nil {
		return
	}
	err := d.CloseTicket(pmKey)
	if err != nil {
		// One immediate retry recovers transient tracker errors.
		err = d.CloseTicket(pmKey)
	}
	if err == nil {
		return
	}
	d.Logger.Warn().Err(err).Str("pm", pmKey).
		Msg("automated post-merge ticket close failed; card flagged for manual close")

	postCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	closeFailed := failureMarker(MarkerCloseFailed, "the automated close of "+pmKey+" failed: "+errors.CauseChain(err))
	closeFailed.Body = strings.TrimSpace("Close this ticket manually to clear the card.\n\n" + closeFailed.Body)
	_ = postMarker(postCtx, d.Commenter, pmKey, closeFailed)
}

// waitForChecks blocks until the PR's CI verdict is conclusive. Passing
// returns nil; failing and a gate timeout return an error carrying the reason.
func (d BoardTransitionDeps) waitForChecks(ctx context.Context, res PRResult) error {
	ticker := time.NewTicker(deployCheckInterval)
	defer ticker.Stop()
	// This wait is the deploy's long silence — minutes with nothing written
	// down, which is what makes an interrupted deploy indistinguishable
	// afterwards from one that never started. A heartbeat every few minutes is
	// enough to tell the two apart without a line per poll.
	d.Logger.Info().Int("pr", res.Number).Msg("deploy: waiting for CI checks")
	polls := 0
	for {
		state, err := d.Deployer.PullRequestChecks(ctx, d.WorkspaceDir, res.Number)
		if err != nil {
			// A read error is not a check verdict: tag it UNKNOWN so the routing
			// reports a credential failure instead of "CI checks failed" and never
			// dispatches the fixer on a green PR (SC-1996).
			return markStateUnreadable(err, "could not read the pull request's check state", "pr", res.URL)
		}
		switch state {
		case forge.ChecksPassing:
			d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Msg("deploy: CI checks passed")
			return nil
		case forge.ChecksFailing:
			d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Msg("deploy: CI checks failed")
			return errors.WithDetails("CI checks failed", "pr", res.URL,
				deployFailingChecksDetail, d.checkNames(res.Number, forge.ChecksFailing))
		}
		polls++
		if polls%deployWaitHeartbeat == 0 {
			d.Logger.Info().Int("pr", res.Number).
				Dur("waited", time.Duration(polls)*deployCheckInterval).
				Msg("deploy: CI still running")
		}
		select {
		case <-ctx.Done():
			return errors.WithDetails("timed out waiting for CI checks", "pr", res.URL,
				deployRunningChecksDetail, d.checkNames(res.Number, forge.ChecksPending))
		case <-ticker.C:
		}
	}
}

// checkNames returns the comma-joined names of the PR's checks whose verdict is
// want, best-effort: a read failure yields "" so the headline degrades to its
// bare reason rather than masking the gate verdict (the SC-1996 rule). It reads
// on a fresh short-lived context because the timeout caller's ctx is already done.
// Note: runVerdict (internal/forge/github/client.go) maps a cancelled check run
// to ChecksPending (SC-2602), so a "still running" headline built from
// ChecksPending can list a check that was actually cancelled, not still in
// flight. Accepted trade-off (plan AD2): it never inverts a verdict, only
// mislabels a rare terminal state as pending.
func (d BoardTransitionDeps) checkNames(number int, want forge.ChecksState) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := d.Deployer.ReadPullRequest(ctx, d.WorkspaceDir, number)
	if err != nil || state == nil {
		return ""
	}
	var names []string
	for _, c := range state.Checks {
		if c.Conclusion == want && c.Name != "" {
			names = append(names, c.Name)
		}
	}
	return strings.Join(names, ", ")
}

// deployFailed posts the failure marker on its own context: the pipeline's
// context may already be cancelled (timeout), and the marker must still land.
func (d BoardTransitionDeps) deployFailed(pmKey, prURL, reason string) error {
	d.Logger.Warn().Str("pm", pmKey).Str("pr", prURL).
		Str("reason", headlineOf(reason)).Msg("deploy: failed")
	postCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := failureMarker(MarkerDeployFailed, reason)
	if prURL != "" {
		m.Fields["pr"] = prURL
	}
	_ = postMarker(postCtx, d.Commenter, pmKey, m, "reason", "pr")
	return errors.WithDetails("deploy failed: "+reason, "pm", pmKey, "pr", prURL)
}

// deployFailedOrDispatchFixer routes a code-fixable deploy failure to the
// automated deploy-fixer instead of redding the card — but only when a launcher
// is wired (the board path, never the CLI) and the per-ticket deploy-fix budget
// has room. Otherwise it falls back to the terminal deploy-failed marker. On a
// successful dispatch it returns nil, releasing the deploy gate while the fixer
// works; the fixer's Stop event drives AdvanceDeployFix, which re-runs the deploy.
func (d BoardTransitionDeps) deployFailedOrDispatchFixer(ctx context.Context, pmKey string, res PRResult, headline string, cause error, branch string) error {
	if d.Launcher != nil {
		if comments, err := d.Commenter.ListComments(ctx, pmKey); err == nil && deployFixRounds(comments) < DefaultDeployFixRounds {
			return d.dispatchDeployFixer(ctx, pmKey, res, branch, headline)
		}
	}
	return d.deployFailed(pmKey, res.URL, deployReason(headline, cause))
}

// dispatchDeployFixer launches the deploy-fixer and, only when one actually
// started, posts the running deploy-fix-started marker (carrying the failure
// headline and the PR binding for the trail). The marker keeps the card spinning
// rather than red while the fixer works — which is only true of a fixer that
// exists, so it follows the launch (SC-4244).
func (d BoardTransitionDeps) dispatchDeployFixer(ctx context.Context, pmKey string, res PRResult, branch, headline string) error {
	launched, err := d.launchDeployFixAgent(ctx, pmKey, deployFixDispatch(pmKey, res.Number, branch))
	if err != nil {
		return err
	}
	if !launched {
		// A fixer on this machine is already working this deploy: its marker and
		// its round stand, and its Stop event re-drives the gate (SC-4244).
		d.Logger.Info().Str("pm", pmKey).
			Msg("board deploy: fixer launch refused, one is already running; leaving its record standing")
		return nil
	}
	m := marker.Marker{
		Type:   MarkerDeployFixStarted,
		Fields: fields("pr", res.URL, "number", strconv.Itoa(res.Number), "branch", branch),
		Body:   headline,
	}
	if err := postMarker(ctx, d.Commenter, pmKey, m, "pr", "number", "branch"); err != nil {
		return errors.WrapWithDetails(err, "posting deploy-fix-started marker", "pm", pmKey)
	}
	return nil
}

// launchDeployFixAgent launches the deploy-fixer fire-and-forget (no claim: driven
// by this daemon's local Stop event, like the PR-loop agents). A launch failure
// reds the card — leaving it spinning would strand the deploy.
func (d BoardTransitionDeps) launchDeployFixAgent(ctx context.Context, pmKey, prompt string) (launched bool, err error) {
	name := agentNameFor(pmKey, deployFixAgentStage)
	started, err := d.launchAgent(ctx, pmKey, name, prompt)
	if err != nil {
		body := markerBody(failureMarker(MarkerDeployFailed, "could not launch the deploy fixer — "+errors.CauseChain(err)))
		_, _ = d.Commenter.AddComment(ctx, pmKey, body)
		return false, errors.WrapWithDetails(err, "launching deploy fixer", "pm", pmKey)
	}
	return started, nil
}

// executePrompt builds the implementation-stage dispatch. The BOARD CONTEXT
// trailer mirrors the bug path's fixer dispatch: a board container holds no
// push credentials and no user — an executor that pauses to ask permission
// burns the whole run and fails the stage with nothing posted (the 1087
// deadlock, three runs in a row).
// planPrompt gates planning behind the ticket review: the last point where a
// ticket that treats a symptom, duplicates an open ticket, or is really a design
// question can still be fixed cheaply. Every later stage takes the ticket as
// given, so without this the pipeline builds whatever it was handed.
//
// The gate acts on its own findings rather than asking — it reframes, links,
// creates the design ticket — so the run continues straight into planning in the
// cases that stay plannable, and stops with the reason recorded in the cases that
// do not. A ticket already carrying a [human:ticket-review] marker skips the gate
// so a planning retry does not re-review it.
//
// A terminal verdict must ALSO post [human:nothing-to-do], because "stop after
// recording it" is not a stop the board can see: the [human:ticket-review]
// marker classifies as a backlog-stage marker, so a card already carrying
// [human:planning-started] still derives planning/running, and the stuck-running
// pass reds it and relaunches — re-reaching the same verdict, forever (SC-3149
// re-planned twelve times overnight on a verdict it got right the first time).
// [human:nothing-to-do] is the planning stage's terminal resolved marker; naming
// it here is what turns a correct verdict into a stop.
func planPrompt(key string) string {
	return "/human-ticket-review " + key +
		" — then, if the verdict is ready or reframed, continue with /human-plan " + key +
		" (a reframed verdict's corrected framing is in the [human:ticket-review] marker; plan against that, not the description)." +
		" If the verdict is superseded, escalated or rejected, there is nothing to plan on this ticket:" +
		" post the terminal marker — human marker post " + key + " nothing-to-do --field \"evidence=<the verdict, and the key that carries the work>\" —" +
		" and then stop. Without it the board reads this run as a crash and re-plans the ticket forever." +
		" Skip the review and go straight to /human-plan " + key + " when the ticket already carries a [human:ticket-review] marker." +
		" BOARD CONTEXT: there is no user to ask — never end the run with a question; act on what you find and record it."
}

func executePrompt(key, extra string) string {
	return "/human-execute " + key + extra +
		" BOARD CONTEXT: do NOT run git push — leave the branch local; the daemon's Deploy stage ships it. There is no user to ask: never end the run with a question — post the review handoff (human handoff post with --branch) or report the failure."
}

// dispatchKey resolves the key an agent is dispatched on: the engineering
// ticket where one exists, else the PM ticket itself (single-tracker topology,
// where the plan lives in a [human:plan] comment).
func dispatchKey(pmKey string, card BoardCard) string {
	if card.EngineeringKey != "" {
		return card.EngineeringKey
	}
	return pmKey
}

// reviewPrompt builds the /human-review dispatch, threading the handoff branch
// and commits as an authoritative binding. The reviewer verifies the
// checked-out code IS this branch and these commits before reviewing, and pins
// its verdict to the dispatched key — so it can never review a stale HEAD and
// post on an unrelated ticket (SC-695). Flags are appended only when present so
// pre-binding handoffs (branch-less/commit-less) still dispatch cleanly.
func reviewPrompt(key string, card BoardCard) string {
	prompt := "/human-review " + key
	if card.Branch != "" {
		prompt += " --branch=" + card.Branch
	}
	if card.Commits != "" {
		prompt += " --commits=" + card.Commits
	}
	return prompt
}

// isReviewRetry mirrors isBuildRetry/isPlanningRetry for the verification stage:
// a failed review is relaunched in place. Failed-state only — a running review
// is protected by the idempotency guard, and a DONE verification with a failing
// verdict takes the rework path instead (SC-695).
func isReviewRetry(to BoardStage, card BoardCard) bool {
	// An outage card is relaunched in place exactly like a failed one (SC-2307),
	// so the reconcile backoff can re-drive it.
	return to == BoardVerification &&
		card.Stage == BoardVerification &&
		(card.State == BoardFailed || card.State == BoardOutage)
}

// isDuplicateDrop reports a drop onto a stage the card is already working, so a
// quick re-drag before the board refetches cannot launch a second agent.
//
// The DERIVED card — not a raw per-stage marker scan — is the authority, and the
// two genuinely disagree: DeriveBoardCard retires a done-stage marker the
// pipeline has moved past (supersededByNewerMarker), a raw scan never does. A PR
// review→fix loop that escalates to a [human:options] block leaves
// [human:pr-fix-started] as the newest done-stage marker forever — the
// escalation posts its block against the implementation stage, and nothing ever
// closes the done stage. Once the chosen rebuild lands and its review passes,
// the board (deriving) offers the Deploy drop while a raw scan still reads
// "running": every drop was swallowed by a nil return no human could see or
// clear (SC-1857). Gating on the derivation the board itself renders keeps the
// two answering the same question.
func isDuplicateDrop(to BoardStage, card BoardCard) bool {
	return card.Stage == to && card.State == BoardRunning
}

// awaitingDecision reports a card paused on an open [human:options] block: its
// only valid continuation is choosing an option (ApplyOption), so every drag is
// refused with an actionable reason instead of the opaque forward-only rejection.
// DeriveBoardCard attaches the block only while it is genuinely open (its
// consumption rules retire a pursued or superseded one), so a set Options slice
// is exactly a live, undecided fork (SC-1857).
func awaitingDecision(card BoardCard) bool {
	return len(card.Options) > 0
}

// launchDecidedStage starts the stage a recorded decision named, with the
// direction that decided it injected into the prompt.
//
// An implementation launch is routed to the self-planning fix pipeline that
// owns the ticket when there is one. An autofix or security-fix run raises its
// decision in PREFLIGHT, before it has written a plan, so handing the answer to
// the plan executor refuses the launch at the plan gate and drives the card into
// planning instead — the SC-2986 class, reappearing on the decision path because
// this was the one relaunch site that never classified the pipeline.
//
// A decision is human-initiated, like the Fix/Retry entry points: the interval
// since the decision became available is the human's think-time, not a pipeline
// wait, so it is suppressed (empty cause, SC-2462). A plan-executing
// implementation launch is still plan-gated like every other (SC-2596).
func (d BoardTransitionDeps) launchDecidedStage(ctx context.Context, pmKey string, stage BoardStage, card BoardCard, comments []tracker.Comment, label string) (bool, error) {
	direction := ""
	if label != "" {
		direction = " — a decision was made on this ticket: pursue the direction in the latest " +
			OptionChosenHeader + " comment (" + label + ")"
	}
	if stage == BoardImplementation {
		if kind := d.classifyFixPipeline(ctx, pmKey, comments); kind != fixNone {
			return d.launchFixPipeline(ctx, pmKey, kind, direction)
		}
	}
	return d.startAgentStage(ctx, pmKey, stage, startedHeaderFor(stage),
		stagePrompt(stage, pmKey, card)+direction, WaitCause(""), stage == BoardImplementation)
}

// choiceLabel reads the answer text off the ticket's latest recorded choice —
// the `<id>: <label>` head of its [human:option-chosen] marker. Empty when the
// ticket carries no choice, which is what a launch with no direction to inject
// looks like.
//
// It exists so a launch the click never made builds the SAME prompt the click
// would have: the pass that starts a decided stage later has only the ticket to
// read the answer from.
func choiceLabel(comments []tracker.Comment) string {
	chosen, ok := latestOptionChosen(comments)
	if !ok {
		return ""
	}
	m, ok := marker.ParseBody(chosen.Body)
	if !ok {
		return ""
	}
	_, label, ok := strings.Cut(m.Head, ":")
	if !ok {
		return strings.TrimSpace(m.Head)
	}
	return strings.TrimSpace(label)
}

// isQueuedLaunch reports a card whose answered decision queued a stage that
// never started, re-dropped on that same stage. Same-stage by definition: the
// queued placement IS the stage the decision named, so this can only ever start
// the stage the record asks for, never move the card somewhere else.
func isQueuedLaunch(to BoardStage, card BoardCard) bool {
	return card.State == BoardQueued && card.Stage == to
}

// isReworkTransition reports the one allowed backward move: re-running the
// build on a card whose review returned a failing verdict — or whose review
// passed without a recorded branch, which has nothing to ship and can only be
// repaired by rebuilding (SC-297).
func isReworkTransition(to BoardStage, card BoardCard) bool {
	return to == BoardImplementation &&
		card.Stage == BoardVerification &&
		card.State == BoardDone &&
		(VerdictFailed(card.Verdict) || card.Branch == "")
}

// isPlanningRetry reports the second sanctioned non-forward move: relaunching
// planning on a card sitting in the planning stage. Failed state is the retry
// case (SC-355); done state is the replan case — a finished plan whose code
// context drifted while the ticket waited in the Engineering backlog gets a
// fresh plan, which supersedes the old one by the plan layer's latest-wins
// rule. A running planning card is protected by ApplyTransition's idempotency
// guard either way.
func isPlanningRetry(to BoardStage, card BoardCard) bool {
	// An outage card is relaunched in place exactly like a failed one (SC-2307),
	// so the reconcile backoff can re-drive it.
	return to == BoardPlanning &&
		card.Stage == BoardPlanning &&
		(card.State == BoardFailed || card.State == BoardDone || card.State == BoardOutage)
}

// isBuildRetry mirrors isPlanningRetry for the implementation stage: failed
// builds only — running builds are protected by the idempotency guard, and a
// verification-stage card takes the rework path instead (SC-591).
func isBuildRetry(to BoardStage, card BoardCard) bool {
	// An outage card is relaunched in place exactly like a failed one (SC-2307),
	// so the reconcile backoff can re-drive it.
	return to == BoardImplementation &&
		card.Stage == BoardImplementation &&
		(card.State == BoardFailed || card.State == BoardOutage)
}

// fixPipeline names which self-planning fix pipeline owns a ticket, if any.
type fixPipeline int

const (
	fixNone     fixPipeline = iota // an ordinary plan-executing build
	fixBug                         // autofix (/human-autofix)
	fixSecurity                    // security-fix (/human-security-fix)
)

// classifyFixPipeline reports which self-planning fix pipeline should own a
// recovery relaunch of the implementation stage. The ticket kind is
// authoritative and covers every interruption point (including one before
// triage posted its verdict): IsSecurity → security-fix, else IsBug → autofix.
// With no Getter (or a fetch blip), it falls back to the marker heuristic — a
// recorded [human:bug-verdict] with no [human:plan] is a bug pipeline
// interrupted mid-run — so a tracker read failure never drops the run back onto
// the plan gate it exists to bypass (SC-2986).
func (d BoardTransitionDeps) classifyFixPipeline(ctx context.Context, pmKey string, comments []tracker.Comment) fixPipeline {
	// A run that recorded its own pipeline identity at start is authoritative and
	// survives a machine restart or a Getter blip. Absence falls through to the
	// ticket-kind Getter and the marker heuristic — so a run in flight from before
	// this landed keeps today's behaviour (SC-2989).
	if p := recordedPipeline(comments); p != fixNone {
		return p
	}
	if d.Getter != nil {
		if issue, err := d.Getter.GetIssue(ctx, pmKey); err == nil && issue != nil {
			switch {
			case issue.IsSecurity():
				return fixSecurity
			case issue.IsBug():
				return fixBug
			default:
				return fixNone
			}
		} else if err != nil {
			d.Logger.Warn().Err(err).Str("pm", pmKey).
				Msg("board relaunch: cannot fetch ticket to classify fix pipeline; falling back to markers")
		}
	}
	if hasBugVerdict(comments) && !hasPlanEvidence(comments) {
		return fixBug
	}
	return fixNone
}

// isDeployRetry reports the deploy-stage twin of isBuildRetry: relaunching the
// deploy pipeline on a card whose deploy failed. Failed-state only — a running
// deploy is protected by ApplyTransition's idempotency guard. The retry rebases
// and re-deploys the already-reviewed branch rather than re-implementing it, so
// a conflicted deploy is never a dead end (735).
func isDeployRetry(to BoardStage, card BoardCard) bool {
	// An outage card is relaunched in place exactly like a failed one (SC-2307),
	// so the reconcile backoff can re-drive it.
	return to == BoardDoneStage &&
		card.Stage == BoardDoneStage &&
		(card.State == BoardFailed || card.State == BoardOutage)
}

// doneBody builds the PR description with the PM→engineering→branch trail.
func doneBody(pmKey string, card BoardCard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PM ticket: %s\n", pmKey)
	if card.EngineeringKey != "" {
		fmt.Fprintf(&b, "Engineering ticket: %s\n", card.EngineeringKey)
	}
	if card.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", card.Branch)
	}
	return b.String()
}

// failedTypeFor returns the *-failed marker TYPE for a stage — what a writer
// needs. Empty for a stage that has no failed marker.
func failedTypeFor(stage BoardStage) string {
	switch stage {
	case BoardPlanning:
		return MarkerPlanningFailed
	case BoardImplementation:
		return MarkerImplementationFailed
	case BoardVerification:
		return MarkerReviewFailed
	case BoardDoneStage:
		return MarkerDeployFailed
	default:
		return ""
	}
}

// failedHeaderFor returns the *-failed marker header for a stage — what a
// reader matches on. Derived from failedTypeFor so the two cannot disagree.
func failedHeaderFor(stage BoardStage) string {
	return headerForType(failedTypeFor(stage))
}

// outageTypeFor returns the *-outage marker TYPE for a stage, mirroring
// failedTypeFor. Empty for a stage that has no relaunch path (SC-2307).
func outageTypeFor(stage BoardStage) string {
	switch stage {
	case BoardPlanning:
		return MarkerPlanningOutage
	case BoardImplementation:
		return MarkerImplementationOutage
	case BoardVerification:
		return MarkerReviewOutage
	case BoardDoneStage:
		return MarkerDeployOutage
	default:
		return ""
	}
}

// outageHeaderFor returns the *-outage marker header for a stage.
func outageHeaderFor(stage BoardStage) string {
	return headerForType(outageTypeFor(stage))
}

// headerForType renders a marker type as the header its readers match, and
// keeps "no marker for this stage" spelled the same on both sides: an empty
// type is an empty header, never "[human:]".
func headerForType(markerType string) string {
	if markerType == "" {
		return ""
	}
	return "[human:" + markerType + "]"
}

// latestStageState returns the latest marker's state within a given stage,
// scanning the comment thread. ok is false when the stage has no markers.
func latestStageState(comments []tracker.Comment, stage BoardStage) (ok bool, state BoardState) {
	var haveLatest bool
	var latest tracker.Comment
	for _, c := range comments {
		st, s, isMarker := ClassifyMarker(c.Body)
		if !isMarker || st != stage {
			continue
		}
		if !haveLatest || commentNewer(c, latest) {
			latest = c
			haveLatest = true
			state = s
		}
	}
	return haveLatest, state
}
