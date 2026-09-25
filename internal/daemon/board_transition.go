package daemon

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
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

// ErrLaunchGateRefused is a launch the doctor's launch gate turned away: the
// host cannot serve it (a dead claude-auth store, no docker). It is a sentinel
// so a caller can tell it from the other launch that started nothing — an
// agent already owning the step — because the two mean opposite things: one is
// work in progress whose own exit drives the next action, the other is a host
// condition that belongs to the machine, posts no marker and moves no item
// (SC-5108).
var ErrLaunchGateRefused = stderrors.New("the launch gate refused this launch")

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
	// It returns the commit it PUBLISHED — "" when the branch was already
	// current and nothing moved. Naming that commit is the whole contract:
	// after a re-push the forge's pull-request resource still carries the
	// previous head for a beat, so every read expressed as "this pull request"
	// answers about the tip the rebase replaced (SC-5395). The caller waits for
	// the forge to report this head before reading any verdict from it.
	EnsureMergeable(ctx context.Context, req PRRequest) (head string, err error)
	// FreshenBranch brings the LOCAL branch current with the base OR with
	// origin before a review round, so the reviewer reads the integrated
	// candidate rather than the branch as it was pushed. Two things move the
	// local ref: when origin/<base> has advanced past the branch it merges
	// the base in (a merge, never a rebase — the fixer's recorded head and
	// the reviewer's head binding stay ancestors) and moves the local ref;
	// and, independently of the base merge, when the local ref carries
	// nothing origin lacks (a repair pushed straight to the forge, or plain
	// reconciliation) it is instead moved to ORIGIN's tip and reported as
	// FreshnessCurrent, so a stale local ref is never read over a repair
	// (SC-5596). A textual conflict on the base merge leaves the branch
	// untouched and is reported as FreshnessConflict for the caller to hand
	// to the deploy fixer before any reviewer runs (SC-5279). A local ref
	// that diverges from origin with each side carrying a change the other
	// lacks cannot be reconciled mechanically: it is refused with a plain
	// error naming both heads (reconcileByContent's divergence refusal),
	// which today's only caller logs and reviews the branch as it stands
	// rather than routing to the deploy fixer. On a clean base merge it also
	// runs the project's fast test tier against the merged result, still in
	// the ephemeral worktree; a red tier is reported as FreshnessTestsFailed,
	// routed to the deploy fixer exactly like a conflict, so a merge that is
	// textually clean but does not build is never handed to the reviewer as
	// the integrated candidate. It never pushes: the daemon publishes the
	// branch at merge time exactly as it does the fixer's commits.
	FreshenBranch(ctx context.Context, req PRRequest) (BranchFreshness, error)
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
	// freshness rebase to handle; a local ref origin has already overtaken (the
	// reconciliation adopts origin's tip instead of pushing) also reports false,
	// because nothing of the fixer's was carried (SC-5596).
	PublishResolvedBranch(ctx context.Context, workspaceDir, branch string) (published bool, err error)
}

// BranchFreshness is FreshenBranch's report of what the base merge found.
type BranchFreshness int

const (
	// FreshnessCurrent: the branch already contains the base tip; nothing moved.
	FreshnessCurrent BranchFreshness = iota
	// FreshnessMerged: the base was merged into the local branch, which now
	// heads at a merge commit the reviewer will read.
	FreshnessMerged
	// FreshnessConflict: the base merge conflicts; the branch is untouched and
	// a fixer must resolve it before a reviewer can read an integrated result.
	FreshnessConflict
	// FreshnessTestsFailed: the base merged cleanly in the ephemeral worktree,
	// but the project's fast test tier is red on the integrated result — a
	// symbol renamed on the base against a new call site on the branch, the
	// commonest form of this drift. The ref is deliberately NOT moved: a
	// fixer must resolve it before a reviewer reads a candidate that does not
	// build (SC-5279 acceptance criterion 1).
	FreshnessTestsFailed
)

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
	// deployNoChecksGrace is how long a head may report NO checks before the
	// gate accepts that the repository has no CI. A head pushed moments ago
	// has none because its CI has not registered yet — the freshness rebase and
	// the deploy-fixer both produce exactly that head — and reading that absence
	// as green is how a candidate merged on its previous head's result while the
	// new head's checks were three seconds old (SC-5083 campaign, F19). Actions
	// registers within a minute or two of a push; a repository with no CI pays
	// the grace once per deploy.
	deployNoChecksGrace = 3 * time.Minute
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
	// Head-binding pacing: the forge's pull-request resource lags a force-push
	// by a beat, and every gate read after the freshness rebase is answered
	// about whatever head it currently carries.
	headPollInterval      = 3 * time.Second
	deployHeadWaitTimeout = 5 * time.Minute
	// mergeReintegrations bounds how often a merge refused for an out-of-date
	// head re-runs the freshness stage. The forge's own lag clears inside
	// mergeRetryTimeout; a refusal that outlives it means a sibling deploy
	// actually landed, and only re-integrating the base clears that (SC-5395).
	mergeReintegrations = 2
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
	// this daemon's host (docker, agent-skills, claude-auth, egress). When it returns a
	// non-empty slice the stage launcher neither claims nor launches — it silently
	// leaves the work for a healthy daemon, and the failure surfaces only on this
	// host (doctor / rail LED), never as a ticket marker (SC-912). nil disables.
	LaunchGate func(ctx context.Context) []DoctorCheck
	// BlockedBy reports the still-open issues pmKey must wait for. It resolves
	// each blocker's real status, so a finished blocker is simply absent from
	// the result — the gate never has to guess what "open" means. nil disables
	// the gate (the package's "nil disables" convention).
	BlockedBy func(ctx context.Context, pmKey string) ([]string, error)
	// LoopStepAlive reports whether the named loop-step agent is running on this
	// machine. A re-drive from the reconcile pass carries no exit event, only a
	// snapshot older than the pass itself; when the thread it re-reads names a
	// step with no record, the step may simply still be running — launched by
	// the hook path after the snapshot was taken — and escalating it reds a
	// card over a live fixer (SC-5120). nil disables the check.
	LoopStepAlive func(agentName string) bool
	// Getter fetches the PM ticket so a recovery relaunch of the implementation
	// stage can tell a self-planning fix pipeline (bug/security — which produces
	// its own plan within the run) from a plan-executing build, and re-dispatch
	// the right path. nil disables kind classification: the relaunch then falls
	// back to the [human:bug-verdict] marker heuristic and finally to the plain
	// build retry (SC-2986).
	Getter tracker.Getter
	// LiveAgents lists the board agents running on this machine, so a starting
	// deploy can tell whether the implementation container still holds the
	// checkout it is about to push from. SC-5476 taught the chain and the orphan
	// reconcile about inline-review liveness and left the LAUNCH blind: since
	// SC-782 the verdict is posted from inside that container minutes before it
	// exits, so "review complete on the ticket" stopped implying "the container
	// is gone" and the deploy raced it (SC-5691). nil disables the interlock (the
	// package's "nil disables" convention) — the bare CLI cannot list agents, and
	// the daemon route always can.
	LiveAgents LiveAgentLister
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

// transitionOrigin says WHO asked for a transition: the board's gesture route or
// the daemon's own relaunch. It is a parameter of the private applyTransition
// rather than a field on BoardTransitionRequest, and that is the whole point of
// it (AD2): the request is decoded off the wire, so a field there would let any
// client claim a person's provenance and mint itself the fix rounds the machine
// is not allowed to mint for itself.
//
// Cause alone cannot answer this. The durable re-drive and the stage retry both
// send an EMPTY Cause (cmd/cmddaemon/daemon.go, the redriveDeploy request), so
// on Cause alone every automatic re-drive of a red deploy would look exactly
// like a person clicking Retry and would re-arm a round on a timer.
type transitionOrigin int

const (
	// originHuman is ApplyTransition: the board's drag and context-menu gestures,
	// and the daemon closures that drive the same entry with a Cause set.
	originHuman transitionOrigin = iota
	// originMachine is ApplyRetryTransition: the daemon relaunching a stage for
	// itself (board_retry.go's StageRetry, reconcileShippedFailures' re-drive).
	// It grants nothing, ever.
	originMachine
)

// humanDeployRetry reports the one shape that earns a fresh deploy-fix round: a
// person's Retry deploy. Both halves are required. origin rules out the machine's
// own relaunch; the empty Cause rules out the machine-driven moves that enter by
// the human door — the build→review chain and the poll-boundary recovery each
// carry one, which is the same discriminator ApplyTransition already relies on at
// its ErrClaimLost fork below.
func humanDeployRetry(origin transitionOrigin, cause WaitCause) bool {
	return origin == originHuman && cause == ""
}

// ApplyTransition advances a card from its current stage to the requested next
// stage. The daemon re-loads live comments and re-derives the card here because
// the UI gate is advisory only — the daemon is the authority on whether a
// forward move is allowed (forward-only, single-step, gated on the prior
// stage's completion). All errors carry details for the client.
func (d BoardTransitionDeps) ApplyTransition(ctx context.Context, req BoardTransitionRequest) error {
	_, err := d.applyTransition(ctx, req, originHuman)
	// A machine-driven move carries a Cause (the build→review chain, a
	// poll-boundary recovery); a person's drag does not. The machine keeps the
	// old silence on a lost claim — the winner starts the stage and nothing is
	// waiting on an answer — while the person is told why their drop started
	// nothing, the same rule the awaiting-decision refusal follows (SC-5094).
	if stderrors.Is(err, ErrClaimLost) && req.Cause != "" {
		return nil
	}
	return err
}

// ApplyRetryTransition is the automatic-retry entry: it additionally reports
// whether a launch actually happened, so the retry accounting never charges an
// attempt for a refusal that started nothing (SC-2989).
func (d BoardTransitionDeps) ApplyRetryTransition(ctx context.Context, req BoardTransitionRequest) (launched bool, err error) {
	launched, err = d.applyTransition(ctx, req, originMachine)
	if stderrors.Is(err, ErrClaimLost) {
		// Nothing started and nothing failed: another daemon has the stage. Reported
		// as a refusal so the attempt is refunded rather than charged to a launch
		// this machine never made (SC-2989).
		return false, nil
	}
	return launched, err
}

// applyTransition is the shared body behind both public entries. It reports
// whether a launch actually happened alongside the error so the retry path can
// tell a genuine relaunch from a refusal that started nothing (SC-2989); the
// error-only ApplyTransition wrapper discards launched for every drag/gesture/
// chain caller that only cares whether the move was accepted.
func (d BoardTransitionDeps) applyTransition(ctx context.Context, req BoardTransitionRequest, origin transitionOrigin) (launched bool, err error) {
	// Ideas never move via board transitions: promotion out of the Ideas
	// column is a label edit, performed by the idea-promote route the desktop
	// calls instead of this one.
	if req.From == BoardIdeas || req.To == BoardIdeas {
		return false, errors.WithDetails("ideas transitions are handled via the idea-promote route",
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
	if handled, launched, err := d.dispatchNonForwardMove(ctx, req, card, comments, origin); handled {
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

	return d.launchForwardStage(ctx, req, card, comments)
}

// dispatchNonForwardMove handles the sanctioned moves that are not a single
// forward step: the one allowed backward move (rework after a failing review)
// and the in-place stage retries (planning, build, review, deploy). Each targets
// a stage the card already derives to, which the forward-only rule would reject
// as a non-advance, so they are resolved here first. handled reports whether the
// request matched one of them — when false, ApplyTransition falls through to the
// forward-only path — and err carries that dispatch's own result.
func (d BoardTransitionDeps) dispatchNonForwardMove(ctx context.Context, req BoardTransitionRequest, card BoardCard, comments []tracker.Comment, origin transitionOrigin) (handled bool, launched bool, err error) {
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
	//
	// A PERSON'S retry additionally re-arms one automated fix round. Without it
	// the gesture was a dead end of its own once the ticket's two rounds were
	// spent: the retry found the same conflict, dispatched nobody, and reposted
	// the identical failure — the one move the board offers reproducing its own
	// failure (SC-5595). The grant is posted BEFORE the stage starts, because the
	// fixer gate reads the thread again from inside the run it is about to start.
	case isDeployRetry(req.To, card):
		if humanDeployRetry(origin, req.Cause) {
			d.grantDeployFixRound(ctx, req.PMKey)
		}
		err := d.runDoneStage(ctx, req, card, comments)
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
func (d BoardTransitionDeps) launchForwardStage(ctx context.Context, req BoardTransitionRequest, card BoardCard, comments []tracker.Comment) (launched bool, err error) {
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
		err := d.runDoneStage(ctx, req, card, comments)
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

// launchGateBlocked reports whether a launch-critical doctor check (docker,
// agent-skills, claude-auth, egress) is currently failing on this daemon's
// host, logging the blocking check so the skip is visible in this host's log.
// EVERY launch path consults this before starting a container — the staged
// launch (startAgentStage), the no-claim PR-loop steps (launchPRLoopAgent) and
// the deploy fixer (launchDeployFixAgent) alike — because a claude-auth store
// the doctor has marked dead stays dead until a fresh login rewrites it: the
// very next launch on any of these paths would walk a container into the same
// refused login, exactly the loop SC-5108 exists to stop (SC-912, SC-5036).
func (d BoardTransitionDeps) launchGateBlocked(ctx context.Context, pmKey string, stage BoardStage) bool {
	if d.LaunchGate == nil {
		return false
	}
	blockers := d.LaunchGate(ctx)
	if len(blockers) == 0 {
		return false
	}
	d.Logger.Warn().
		Str("pm", pmKey).Str("stage", string(stage)).Str("check", blockers[0].ID).
		Msg("board stage launch skipped: launch-critical doctor check failing; leaving work for a healthy daemon")
	return true
}

// requiresPlan declares that this launch executes a pre-written plan, so the
// stage must not start on a ticket that has none (SC-2596). It is true for every
// route that carries out a plan (the forward drag into implementation, the
// rework and build retries, an implementation-stage option relaunch) and false
// for the self-contained fix pipelines (autofix, security-fix), which produce
// their plan within the run.
func (d BoardTransitionDeps) startAgentStage(ctx context.Context, pmKey string, stage BoardStage, startedHeader, prompt string, cause WaitCause, requiresPlan bool) (launched bool, err error) {
	// Launch gate: refuse before the claim so NO [human:claim] is posted — the
	// work is left unclaimed for a healthy daemon and the failure surfaces only
	// on this host, never as a ticket marker (SC-912).
	if d.launchGateBlocked(ctx, pmKey, stage) {
		return false, nil
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
	// A loser starts nothing and leaves the started marker and the launch to the
	// winning daemon — always logged here, and reported to the caller as
	// ErrClaimLost so a person who asked for the launch is told why (SC-5094).
	won, winner, err := d.winClaim(ctx, pmKey, stage)
	if err != nil {
		return false, err
	}
	if !won {
		// Losing is not a failure — the winner is starting the work — but it must
		// not be invisible. Silence here is what made a drag onto a stage this
		// daemon had locked itself out of look like a no-op for five minutes
		// (SC-5094); the two public entries decide who hears about it.
		d.Logger.Info().Str("pm", pmKey).Str("stage", string(stage)).
			Str("winning claim", winner.ID).Str("winning daemon", winner.DaemonID).
			Msg("board stage launch refused: another claim for this stage won the race; leaving the launch to it")
		return false, claimLostError(pmKey, stage, winner)
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
	// A lost claim here is another daemon driving the same card into planning —
	// the drive happened, just not on this machine. Reporting it as this
	// implementation refusal's error would name the wrong stage (SC-5094).
	if _, err := d.startAgentStage(ctx, pmKey, BoardPlanning, PlanningStartedHeader, planPrompt(pmKey), WaitCauseRetry, false); err != nil && !stderrors.Is(err, ErrClaimLost) {
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
// and the loop reports its progress via markers. The empty-branch guard stands
// — a card no marker names a branch for has nothing to open — but it is no
// longer asked of the handoff alone.
func (d BoardTransitionDeps) runDoneStage(_ context.Context, req BoardTransitionRequest, card BoardCard, comments []tracker.Comment) error {
	branch := doneStageBranch(comments, card)
	if branch == "" {
		_ = postMarker(context.Background(), d.Commenter, req.PMKey, marker.Marker{
			Type:   MarkerDeployFailed,
			Fields: fields("reason", "no branch recorded on this ticket — no handoff, deploy or deploy-fix marker names one"),
		})
		return errors.WithDetails("no branch recorded for deploy", "pm", req.PMKey)
	}
	// The resolved branch travels on the card the loop is started with, so
	// openDraftPRAndReview pushes the branch the deploy recorded rather than the
	// empty handoff one. card is a value copy; nothing outside this call sees it.
	card.Branch = branch
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
	// The interlock, first: everything below this line writes to the checkout —
	// the push, and FreshenBranch moving the local branch ref — and the
	// implementation container may still be working in it. Refusing is not an
	// option here (nothing re-drives a reviewed card into the done stage), so the
	// stage waits; past the bound it abandons the launch without a marker, since
	// a container hung that long is the stuck-running sweep's to answer (SC-5691).
	if err := d.awaitCheckoutFree(ctx, pmKey); err != nil {
		d.Logger.Warn().Err(err).Str("pm", pmKey).
			Msg("board PR loop: abandoning the deploy launch; the checkout is still held")
		return err
	}
	if d.Deployer.BranchMerged(ctx, d.WorkspaceDir, card.Branch) {
		if d.recordDeployedBestEffort(ctx, pmKey, marker.Marker{
			Type:   MarkerDeployed,
			Fields: fields("merged", "already in the base branch; no new PR opened"),
		}) {
			d.closeTicketBestEffort(pmKey)
		}
		return nil
	}
	// The title is only used on a fresh create; the approval path adopts.
	res, err := d.openDraftPR(ctx, pmKey, card.Branch, pmKey, doneBody(pmKey, card, card.Branch))
	if err != nil {
		return err
	}
	_, err = d.launchPRReview(ctx, pmKey, res, card.Branch)
	return err
}

// openDraftPR pushes the branch and opens its pull request in draft, or adopts
// the one already open for it. Shared by both routes into the review loop — the
// board's Deploy drop and `human deploy` — so the interlock is opened the same
// way wherever the loop begins. A failure is recorded as the deploy's failure,
// because to the ticket that is what it is.
func (d BoardTransitionDeps) openDraftPR(ctx context.Context, pmKey, branch, title, body string) (PRResult, error) {
	res, err := d.Deployer.PushAndCreatePR(ctx, PRRequest{
		WorkspaceDir: d.WorkspaceDir,
		Branch:       branch,
		Title:        title,
		Body:         body,
		Draft:        true,
	})
	if err == nil {
		return res, nil
	}
	if reason, ok := secretStoreFailureHeadline(err); ok {
		return res, d.deployFailed(pmKey, "", deployReason(reason, err))
	}
	return res, d.deployFailed(pmKey, "", deployReason(
		"could not push "+branch+" and open its draft pull request — check the branch and forge access, then re-run Deploy", err))
}

// DeployFixBeforeReviewField marks a deploy-fix-started marker whose fixer was
// dispatched by the pre-review base merge rather than by the CI gate. The
// fixer's done exit reads it to decide what comes next: a fixer sent before
// the review hands back to the reviewer, one sent by the CI gate re-runs the
// deploy (SC-5279).
const DeployFixBeforeReviewField = "before"

// deployFixBeforeReviewValue is the field's one value; the field's presence is
// the signal, the value says what it preceded.
const deployFixBeforeReviewValue = "review"

// DeployFixGrantField marks a deploy-fix-started marker whose round was funded
// by a person's Retry deploy rather than by the ticket's own budget. It follows
// DeployFixBeforeReviewField exactly — optional, appended to the field order
// only when set, read back by name — so the marker stays a name-addressed record
// and no reader acquires a dependency on where the line sits (SC-5595).
const DeployFixGrantField = "grant"

// deployFixGrantValue is the field's one value; the field's presence is the
// signal, the value says what funded the round.
const deployFixGrantValue = "retry"

// deployFixIsGrantFunded reports whether one deploy-fix-started BODY records a
// grant-funded round. It takes the body rather than the thread because
// deployFixGrants must ask it of each marker in turn, not only of the newest —
// which is what separates it from deployFixWasBeforeReview below.
func deployFixIsGrantFunded(body string) bool {
	m, parsed := marker.ParseBody(body)
	return parsed && m.Fields[DeployFixGrantField] == deployFixGrantValue
}

// launchPRReview is the one way a review round starts. Before the reviewer is
// launched the branch is brought current with the base, so what the reviewer
// reads is the integrated candidate — the thing that will merge — rather than
// the branch as it was pushed. Run 3 of the campaign spent a review round on
// "branch behind main" on four of six pull requests: the reviewer found the
// drift, the fixer merged the base, the reviewer read everything again. That
// round is mechanical, and a conflict on it — or a clean merge that leaves
// the fast test tier red — is the deploy fixer's job, not a finding. A
// freshen that fails for any reason other than those two is logged and the
// review runs on the branch as it is: the CI gate's own freshness rebase
// still stands behind it, so nothing merges stale.
//
// dispatchedFixer tells the caller which step actually started: true means
// the deploy fixer was dispatched INSTEAD of a reviewer (or the card reds
// when the launch or the fix budget refuses it — either way no review round
// started), false means a reviewer step is the owner of the stage, whether
// freshly launched here or already running from an earlier call. A caller
// that reports "review started" on every nil error — as reviewThenShip once
// did — misreports the fixer-dispatch case as a review in progress.
func (d BoardTransitionDeps) launchPRReview(ctx context.Context, pmKey string, res PRResult, branch string) (dispatchedFixer bool, err error) {
	fresh, freshErr := d.Deployer.FreshenBranch(ctx, PRRequest{WorkspaceDir: d.WorkspaceDir, Branch: branch})
	if freshErr != nil {
		d.Logger.Warn().Err(freshErr).Str("pm", pmKey).Str("branch", branch).
			Msg("board PR loop: could not bring the branch current with the base; reviewing it as it is")
	}
	// FreshnessCurrent and FreshnessMerged both mean the branch is ready to
	// review as it now stands and need no case body — but they are named
	// here rather than left to a `default`, so a fifth BranchFreshness member
	// fails the build instead of silently falling into "launch the reviewer"
	// (SC-3376 is exactly this failure mode for a different closed set).
	//exhaustive:enforce
	switch fresh {
	case FreshnessConflict:
		conflict := errors.WithDetails("the base advanced past the branch and the merge conflicts", "pm", pmKey, "branch", branch)
		return true, d.deployFailedOrDispatchFixer(ctx, pmKey, res,
			"branch behind the base with a conflict — resolving it before the review", conflict, branch, true)
	case FreshnessTestsFailed:
		redTier := errors.WithDetails("the fast test tier failed on the branch merged with the current base", "pm", pmKey, "branch", branch)
		return true, d.deployFailedOrDispatchFixer(ctx, pmKey, res,
			"the fast test tier failed on the branch merged with the current base — resolving it before the review", redTier, branch, true)
	case FreshnessCurrent, FreshnessMerged:
	}
	_, err = d.launchPRLoopAgent(ctx, pmKey, prReviewAgentStage,
		prReviewDispatch(pmKey, res.Number, branch),
		prReviewStartedBody(res.URL, res.Number, branch))
	return false, err
}

// deployFixWasBeforeReview reports whether the newest deploy-fix-started
// marker carries the before-review field — the record that decides where the
// fixer's done exit continues.
func deployFixWasBeforeReview(comments []tracker.Comment) bool {
	started, ok := latestCommentWithHeader(comments, DeployFixStartedHeader)
	if !ok {
		return false
	}
	m, parsed := marker.ParseBody(started.Body)
	return parsed && m.Fields[DeployFixBeforeReviewField] == deployFixBeforeReviewValue
}

// prReviewStartedBody carries the loop's PR binding on the started marker so the
// Stop-hook driver can recover (url, number, branch) without a forge lookup.
func prReviewStartedBody(url string, number int, branch string) string {
	return markerBody(marker.Marker{
		Type:   MarkerPRReviewStarted,
		Fields: fields("pr", url, "number", strconv.Itoa(number), "branch", branch),
	}, "pr", "number", "branch")
}

// prReviewPassedBody binds the loop's approval to what it approved: the branch
// and the head the reviewer read. An approval is evidence about one revision,
// and a later `human deploy` may reuse it only for that revision — a bare
// header would say "approved" about whatever the branch carries by then. The
// head is omitted when the reviewer recorded none, which leaves the marker as
// the record of convergence it always was and makes it reusable by nothing.
func prReviewPassedBody(branch, head string) string {
	f := fields("branch", branch)
	if head = strings.TrimSpace(head); head != "" {
		f["head"] = head
	}
	return markerBody(marker.Marker{Type: MarkerPRReviewPassed, Fields: f}, "branch", "head")
}

func prReviewDispatch(pmKey string, number int, branch string) string {
	return "/human-pr-review " + pmKey + " --pr=" + strconv.Itoa(number) + " --branch=" + branch
}

// prFixStartedBody records which finding the fixer is being sent, so the next
// review round can tell "the same problem again" from "a new problem": the
// former ends the loop, the latter is progress (SC-5174). No finding leaves
// the bare header, and that round can only end on the outer round cap.
func prFixStartedBody(finding, class string) string {
	if strings.TrimSpace(finding) == "" {
		return PRFixStartedHeader
	}
	f := fields("finding", finding)
	if strings.TrimSpace(class) != "" {
		f["class"] = class
	}
	return markerBody(marker.Marker{Type: MarkerPRFixStarted, Fields: f}, "finding", "class")
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

// deployFixRounds counts the deploy-fix rounds the budget has actually been
// charged for: one per deploy-fix-started marker, MINUS every round whose fixer
// ended in an outage.
//
// The refund is not bookkeeping niceness. The budget is spent at DISPATCH — the
// marker is posted when the fixer launches — and the uncharged outage re-drive
// re-enters the whole done stage, so it dispatches a fresh fixer and posts a
// fresh started marker on every reconcile tick. Without this, a substrate that
// stays down for two ticks spends the entire 2-round budget on rounds that
// attempted nothing and the card reds anyway: SC-2307's "an outage costs time and
// nothing else" broken by the counter rather than by the classifier (SC-5592).
//
// Chronological, with a pending flag rather than two independent counts: only an
// outage that FOLLOWS a started marker refunds that marker's round, so a stray or
// repeated outage marker can never drive the count below the rounds that really
// ran. Same started/terminal pairing as lateResultCandidates.
func deployFixRounds(comments []tracker.Comment) int {
	sorted := make([]tracker.Comment, len(comments))
	copy(sorted, comments)
	sort.SliceStable(sorted, func(i, j int) bool { return commentNewer(sorted[j], sorted[i]) })

	n, charged := 0, false
	for _, c := range sorted {
		trimmed := strings.TrimSpace(c.Body)
		switch {
		case strings.HasPrefix(trimmed, DeployFixStartedHeader):
			n++
			charged = true
		case strings.HasPrefix(trimmed, DeployOutageHeader):
			if charged {
				n--
				charged = false
			}
		}
	}
	return n
}

// deployFixGrants counts the re-armed deploy-fix rounds standing UNSPENT on the
// ticket: one per [human:deploy-retry] marker a person's Retry deploy minted,
// minus each one a deploy-fix-started marker carrying `grant: retry` has since
// spent, plus one back where that grant-funded round ended in an outage — the
// same refund deployFixRounds makes for a charged round, for the same reason
// (SC-5592): an outage attempted nothing, so it must not consume the one round
// a person asked for.
//
// A separate counter rather than a term inside deployFixRounds, deliberately.
// deployFixRounds answers "how many rounds ran", which is what the card's
// sentence reports and what the bound is stated in; this answers "may another
// one start". Folding them would make the reported count lie by the number of
// grants (AD4).
//
// Chronological with a pending flag, the same walk as deployFixRounds: only an
// outage FOLLOWING a grant-funded started marker refunds, so a stray or
// repeated outage marker can never mint a grant nobody asked for.
func deployFixGrants(comments []tracker.Comment) int {
	sorted := make([]tracker.Comment, len(comments))
	copy(sorted, comments)
	sort.SliceStable(sorted, func(i, j int) bool { return commentNewer(sorted[j], sorted[i]) })

	n, spent := 0, false
	for _, c := range sorted {
		trimmed := strings.TrimSpace(c.Body)
		switch {
		case strings.HasPrefix(trimmed, DeployRetryHeader):
			n++
		case strings.HasPrefix(trimmed, DeployFixStartedHeader):
			// A round the ticket's own budget funded leaves the grant alone: the
			// grant is spent by the round it PAID for, never by a round that ran
			// while the budget still had room.
			if deployFixIsGrantFunded(trimmed) && n > 0 {
				n--
				spent = true
			} else {
				spent = false
			}
		case strings.HasPrefix(trimmed, DeployOutageHeader):
			if spent {
				n++
				spent = false
			}
		}
	}
	return n
}

// deployRetryGrantBody is a FIXED sentence. Nothing reads anything out of this
// marker but its header, and interpolating a key, a count or a time would make
// the body a format with readers it does not have — the stored-format dependency
// this plan's Dependents section exists to keep from being created by accident.
const deployRetryGrantBody = "a person re-ran Deploy on this card — one further automated fix round is granted for it."

// grantDeployFixRound records the re-armed round. Best-effort on purpose: a
// tracker that refuses the comment must not refuse the retry, because the retry
// is worth running without the grant — the freshness rebase may be all it needed.
func (d BoardTransitionDeps) grantDeployFixRound(ctx context.Context, pmKey string) {
	if err := postMarker(ctx, d.Commenter, pmKey, marker.Marker{
		Type: MarkerDeployRetry,
		Body: deployRetryGrantBody,
	}); err != nil {
		d.Logger.Warn().Err(err).Str("pm", pmKey).
			Msg("board deploy retry: could not record the re-armed fix round; the retry proceeds on the spent budget")
	}
}

// launchPRLoopAgent launches one loop step's agent (fire-and-forget, no claim:
// the loop is driven by the launching daemon's local Stop events) and records
// the step on the ticket ONLY when an agent actually started. The started body
// is passed in rather than posted by the caller because the order is the point:
// a marker written ahead of a refused launch is a loop step the thread claims
// and nothing performed (SC-4244). A launch failure escalates the card —
// leaving it spinning would strand the loop.
func (d BoardTransitionDeps) launchPRLoopAgent(ctx context.Context, pmKey string, stage BoardStage, prompt, startedBody string) (launched bool, err error) {
	// Launch gate: same refusal startAgentStage applies, extended to the no-claim
	// loop steps — a dead claude-auth store must refuse the NEXT reviewer/fixer
	// launch too, not just the stage that first recorded the refusal (SC-5108).
	if d.launchGateBlocked(ctx, pmKey, stage) {
		return false, errors.WrapWithDetails(ErrLaunchGateRefused, "launch gate refused the PR "+string(stage)+" agent", "pm", pmKey, "stage", string(stage))
	}
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

// deployFixLoopNumber recovers the PR number a before-review deploy-fix
// handback should review against: the deploy-fix-started marker that
// dispatched this very fixer carries the real (number, url) binding
// (dispatchDeployFixer), so read that first. A conflict found before the
// FIRST review round leaves no pr-review-started marker behind — no reviewer
// has launched yet, so prLoopNumber alone reads 0 and the handback would
// dispatch the reviewer against PR #0 (SC-5279 follow-up to SC-5119).
func deployFixLoopNumber(comments []tracker.Comment) int {
	if n, err := strconv.Atoi(strings.TrimSpace(latestPrefixedLine(comments, DeployFixStartedHeader, "number:"))); err == nil {
		return n
	}
	return prLoopNumber(comments)
}

// deployFixLoopURL is deployFixLoopNumber's URL counterpart.
func deployFixLoopURL(comments []tracker.Comment) string {
	if url := strings.TrimSpace(latestPrefixedLine(comments, DeployFixStartedHeader, "pr:")); url != "" {
		return url
	}
	return prLoopURL(comments)
}

// doneStageBranch is the branch the done stage is working on, read from
// whichever marker actually recorded it.
//
// The handoff was once the sole source and three routes never write one:
// `human deploy --branch` records the branch on [human:deploy-started]
// (deployStartedBody) and the fixer's dispatch on [human:deploy-fix-started]
// (dispatchDeployFixer). A loop started from the CLI therefore approved and
// merged an empty branch (SC-5119, fixed at AdvancePRLoop and then at
// AdvanceDeployFix), and a deploy retry refused outright with "no branch
// recorded on ready-for-review handoff" about a branch written plainly on the
// deploy's own start marker (SC-5396, the third and last site).
//
// The order is by SOURCE, not by recency: the loop's own start marker is the
// binding it already trusts for the PR number and URL, and the handoff is the
// weakest — it names what implementation handed over, which a later deploy may
// have overridden.
//
// Source precedence only holds within ONE round: a ticket that reached the
// done stage once, went back through implementation (start-implementation and
// start-fix-run from `stopped` are both declared transitions), and handed off
// a DIFFERENT branch now carries a stale PRReviewStartedHeader/
// DeployFixStartedHeader/DeployStartedHeader from the earlier round. Trusting
// it here — as source precedence alone would — deploys the old branch, and if
// that branch is already on the base the engine's already-merged carve-out
// silently records the new work as shipped. currentApproval guards the
// identical case for the approval marker (deploy_entry.go:265-280); a binding
// older than the newest [human:ready-for-review] handoff is ignored the same
// way here, before source precedence is applied (SC-5396) — but only when
// that handoff IS a later round. One re-posted AFTER a verdict, to record the
// reviewer's own commit, names nothing that verdict did not judge, and
// letting it win reverts a live deploy to the branch implementation handed
// over (SC-5475) — handoffIsBookkeepingRepost is what tells that repost apart
// from an ordinary rework's handoff, which precedes the verdict it results in
// and so must still win.
func doneStageBranch(comments []tracker.Comment, card BoardCard) string {
	handoff, hasHandoff := latestCommentWithHeader(comments, ReadyForReviewHeader)
	newRound := hasHandoff && !handoffIsBookkeepingRepost(comments)
	for _, header := range []string{PRReviewStartedHeader, DeployFixStartedHeader, DeployStartedHeader} {
		c, ok := latestCommentWithHeader(comments, header)
		if !ok || (newRound && commentNewer(handoff, c)) {
			continue
		}
		if branch := strings.TrimSpace(parsePrefixedLine(c.Body, "branch:")); branch != "" {
			return branch
		}
	}
	return card.Branch
}

// AdvancePRLoop is the deploy-stage loop executor. On each reviewer/fixer exit
// the failure watcher calls it with the outcome the step recorded in the state
// store (reviewVerdict or fixExit); it reads the loop's markers, asks the pure
// decider for the next action, and executes it: launch the reviewer, launch the
// fixer, un-draft + merge via the existing DeployBranch, or red the card for a
// human. Human PR review runs out of band and never enters here.
func (d BoardTransitionDeps) AdvancePRLoop(ctx context.Context, pmKey string, outcome PRLoopOutcome) error {
	if !beginPRLoopDrive(pmKey) {
		d.Logger.Info().Str("pm", pmKey).
			Msg("board PR loop: a drive is already in flight for this ticket; standing down")
		return nil
	}
	defer endPRLoopDrive(pmKey)
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading comments for PR loop", "pm", pmKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	if d.loopStepStillRunning(pmKey, comments, outcome) {
		d.Logger.Info().Str("pm", pmKey).
			Msg("board PR loop: re-drive found the current step unrecorded but its agent alive; leaving it to finish")
		return nil
	}
	number, url, branch := prLoopNumber(comments), prLoopURL(comments), doneStageBranch(comments, card)
	// FindingRepeated only means something at the review stage: it asks whether
	// THIS review's finding is the one the fixer was just sent. On a fix-stage
	// drive outcome.ReviewFinding still carries the LAST review's fingerprint —
	// the same value that launched this very fixer via prFixStartedBody below —
	// so comparing it here would always read true and falsely blame a
	// fix-stage escalation (a crashed fixer, an unclassifiable exit) on a
	// repeated finding it never re-reviewed (SC-5174).
	if LatestPRLoopStage(comments) == PRStageReview {
		byIdentity := findingRepeated(comments, outcome.ReviewFinding)
		byClass := classRepeated(comments, outcome.ReviewClass)
		outcome.FindingRepeated = byIdentity || byClass
		outcome.ClassRepeated = byClass && !byIdentity
	}
	switch EvaluatePRLoop(comments, outcome) {
	case PRActionReview:
		_, err := d.launchPRReview(ctx, pmKey, PRResult{Number: number, URL: url}, branch)
		return err
	case PRActionFix:
		_, err := d.launchPRLoopAgent(ctx, pmKey, prFixAgentStage,
			prFixDispatch(pmKey, number, branch), prFixStartedBody(outcome.ReviewFinding, outcome.ReviewClass))
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
		if _, err := d.Commenter.AddComment(ctx, pmKey, prReviewPassedBody(branch, outcome.ReviewHead)); err != nil {
			d.Logger.Warn().Err(err).Str("pm", pmKey).
				Msg("board PR loop: could not record the passing review; continuing to the merge")
		}
		// A branch already on the base has no draft left to release: the forge
		// refuses to un-draft a merged pull request, and reporting that refusal
		// as a deploy failure reds a card whose work shipped. The engine's own
		// already-merged carve-out records the outcome instead.
		if !d.Deployer.BranchMerged(ctx, d.WorkspaceDir, branch) {
			if err := d.Deployer.MarkReadyForReview(ctx, d.WorkspaceDir, number); err != nil {
				return d.deployFailed(pmKey, url, deployReason(
					"the reviewed PR could not be marked ready for merge — open the PR and mark it ready, then re-run Deploy", err))
			}
		}
		// Reuse the untouched deploy engine: it adopts the now-ready open PR
		// (forge.AdoptOrCreatePullRequest), runs the CI gate, freshness rebase and merge.
		return d.DeployBranch(ctx, pmKey, pmKey, doneBody(pmKey, card, branch), branch)
	default: // PRActionEscalate
		return d.escalatePRLoop(ctx, pmKey, comments, outcome)
	}
}

// loopStepStillRunning is the re-drive's liveness check. Only a drive with no
// exit event behind it (outcome.Agent empty) asks: the hook path's drive is
// the step's own ending and needs no probe. A step with no record whose agent
// is alive is work in progress, not a finished step the loop cannot read.
//
// Two questions, because the done stage runs three agents and only two of them
// are loop steps. The second is the deploy fixer's, and it deliberately sits
// OUTSIDE loopHalfStillRunning's stepRecorded short-circuit: the merge that
// dispatched the fixer runs inside the approved review's own drive, so the
// newest loop marker still names that review and its recorded approval would
// otherwise send this re-drive straight back to PRActionMerge, against the
// branch the fixer is mid-rebase on (SC-5591).
func (d BoardTransitionDeps) loopStepStillRunning(pmKey string, comments []tracker.Comment, outcome PRLoopOutcome) bool {
	if d.LoopStepAlive == nil || outcome.Agent != "" {
		return false
	}
	if d.loopHalfStillRunning(pmKey, comments, outcome) {
		return true
	}
	return d.LoopStepAlive(agentNameFor(pmKey, deployFixAgentStage))
}

// loopHalfStillRunning answers the question for the loop's own two steps: the
// step the fresh thread names has no record of its own and its agent is alive.
// The caller has already established that d.LoopStepAlive is wired and that this
// drive carries no exit event.
func (d BoardTransitionDeps) loopHalfStillRunning(pmKey string, comments []tracker.Comment, outcome PRLoopOutcome) bool {
	stage := LatestPRLoopStage(comments)
	// A record from a PRIOR round still satisfies stepRecorded — those keys are
	// never cleared between rounds — so a recorded-but-stale outcome must be
	// treated as unrecorded here too, or every round after the first skips the
	// alive check and escalates over a live fixer/reviewer (SC-5120).
	if outcome.stepRecorded(stage) && !outcome.stepStale(stage) {
		return false
	}
	var agentStage BoardStage
	switch stage {
	case PRStageReview:
		agentStage = prReviewAgentStage
	case PRStageFix:
		agentStage = prFixAgentStage
	default:
		return false
	}
	return d.LoopStepAlive(agentNameFor(pmKey, agentStage))
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
	stage := LatestPRLoopStage(comments)
	if stage == PRStageFix && outcome.FixExit != PRFixDone {
		switch opts := outcome.FixOptions; {
		case len(opts) >= marker.MinDecisionOptions:
			m, order := optionsMarker(BoardImplementation, decisionContext(outcome), opts)
			return postMarker(ctx, d.Commenter, pmKey, m, order...)
		case len(opts) == 1 && prReviewRounds(comments) < MaxSoleDirectionPursuits:
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
	case outcome.ReviewVerdict == PRVerdictChanges && outcome.ClassRepeated:
		return "the machine review found the same class of blocking problem twice in one file and the fixer did not close it — " + outcome.ReviewClass + " — fix it yourself, then re-run Deploy"
	case outcome.ReviewVerdict == PRVerdictChanges && outcome.FindingRepeated:
		return "the machine review found the same blocking problem twice and the fixer did not resolve it — " + outcome.ReviewFinding + " — fix it yourself, then re-run Deploy"
	case outcome.ReviewVerdict == PRVerdictChanges:
		return "the machine review did not converge within " + strconv.Itoa(DefaultPRReviewRounds) + " review rounds — review the PR yourself, then re-run Deploy"
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

// Blocker is what a needs-human-work stop recorded about itself in the stage
// record (shared/exit-contract.md, SC-5179): the kind of blocker, the evidence
// observed, what was attempted, and the condition that releases the work.
type Blocker struct {
	Kind      string `json:"kind"`
	Evidence  string `json:"evidence"`
	Attempted string `json:"attempted"`
	Release   string `json:"release"`
}

// addTo returns the marker with the non-empty blocker fields added, under the
// names the marker protocol declares for every *-failed marker
// (marker.BlockerFields). The field map is copied, so the value semantics the
// signature promises hold even for a caller that keeps using its own marker.
//
// kind is coerced to "other" when it is not one of marker.BlockerKinds(): this
// is the one machine path that puts an agent-supplied kind on a *-failed
// marker without going through `human marker post`'s refusal, and postMarker
// logs-and-posts rather than drops an invalid marker (SC-3889), so a
// misspelled or invented kind would otherwise reach the ticket looking
// classified while nothing downstream could group it (SC-5250).
func (b Blocker) addTo(m marker.Marker) marker.Marker {
	fields := make(map[string]string, len(m.Fields)+4)
	maps.Copy(fields, m.Fields)
	kind := strings.TrimSpace(b.Kind)
	if kind != "" && !slices.Contains(marker.BlockerKinds(), kind) {
		kind = "other"
	}
	for k, v := range map[string]string{"kind": kind, "evidence": b.Evidence, "attempted": b.Attempted, "release": b.Release} {
		if v = strings.TrimSpace(v); v != "" {
			fields[k] = v
		}
	}
	m.Fields = fields
	return m
}

// DeployFixReport is what the deploy fixer recorded in stage.deploy-fix, as its
// driver needs it. The three travel as one value because they are one record
// read in one place (readDeployFixReport), and the next field the loop needs
// should not become a fourth positional argument.
type DeployFixReport struct {
	// Exit is the class the fixer recorded; "" when it recorded nothing, which
	// the driver treats like any other non-done ending.
	Exit StageExit
	// Blocker is what a needs-human-work stop recorded about itself; the loop
	// carries it onto the marker it posts on the fixer's behalf (SC-5179).
	Blocker Blocker
	// Summary is the fixer's one line about its stop. On an outage it is the only
	// place the unreachable substrate is named — an outage carries no blocker by
	// contract (shared/exit-contract.md) — so it becomes the outage marker's
	// reason and, through DeriveBoardCard, the card's face (SC-5592).
	Summary string
}

// AdvanceDeployFix is the deploy-fixer's Stop-event driver. On the fixer's exit the
// failure watcher calls it with the report the agent recorded in stage.deploy-fix. A
// `done` exit publishes the fixer's local resolution and re-runs the deploy pipeline
// (the branch is then ready for a fresh CI gate + merge); an `outage` exit is not a
// failure at all and parks the card on the done stage's outage marker (SC-5592); any
// other exit reds the card with a terminal deploy-failed. The deployFixRounds budget
// already bounds how many times the pipeline re-enters here, so a genuinely
// unfixable failure terminates. The blocker is what a needs-human-work stop recorded
// about itself; the loop carries it onto the marker it posts on the agent's behalf,
// so the person on the red card is not sent back to re-run the investigation.
func (d BoardTransitionDeps) AdvanceDeployFix(ctx context.Context, pmKey string, report DeployFixReport) error {
	fixExit := report.Exit
	comments, err := d.Commenter.ListComments(ctx, pmKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading comments for deploy fix", "pm", pmKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)
	branch := doneStageBranch(comments, card)
	if fixExit == ExitDone {
		// The fixer resolved the conflict in a container that holds no push
		// credentials, exactly like every other board fixer — so its deliverable is
		// the local branch, and publishing it is the daemon's job. Publishing before
		// the deploy runs is what makes the resolution visible at all: the deploy
		// reads the branch from origin (branchTip prefers the origin ref), so an
		// unpublished resolution would be silently discarded and the same conflict
		// re-hit (SC-2845).
		//
		// The branch comes from doneStageBranch, not card.Branch directly: a
		// handoff-less loop (`human deploy --branch`) reaches the deploy-fixer with
		// the correct branch (dispatchDeployFixer takes it as a parameter), but
		// card.Branch is filled only from the ready-for-review handoff and is empty
		// here — the same empty-branch failure SC-5119 fixed one step earlier
		// (SC-5119 follow-up).
		if _, err := d.Deployer.PublishResolvedBranch(ctx, d.WorkspaceDir, branch); err != nil {
			return d.deployFailed(pmKey, "", deployReason(
				"the deploy fixer's resolution could not be published to "+branch+" — check the branch, then re-run Deploy",
				err))
		}
		// A fixer the pre-review base merge dispatched resolved a conflict the
		// reviewer has not read yet: the review is what comes next, on the
		// integrated branch. Only the CI gate's fixer re-runs the deploy (SC-5279).
		if deployFixWasBeforeReview(comments) {
			_, err := d.launchPRReview(ctx, pmKey, PRResult{Number: deployFixLoopNumber(comments), URL: deployFixLoopURL(comments)}, branch)
			return err
		}
		return d.DeployBranch(ctx, pmKey, pmKey, doneBody(pmKey, card, branch), branch)
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
	// A substrate the fixer could not reach is not a broken deploy: nothing about
	// the branch is wrong and nothing was attempted, so the rule since SC-2307 is
	// to wait rather than red. Posting the done stage's outage marker is what puts
	// the card in BoardOutage, where reconcileOutage re-drives it on its own
	// interval charging nothing (isDeployRetry accepts an outage card) and hands it
	// to a person only past OutageWaitBound. Deliberately AFTER the
	// stageAlreadyFailed guard, unlike handleBoardAgentExit's outage gate: there the
	// standing failure is usually the one the exiting skill posted itself, while the
	// deploy fixer posts no marker at all — so a deploy-failed newer than this
	// round's dispatch is another actor's red that a person now owns, and flipping it
	// back to "waiting" would re-drive the deploy underneath them (SC-5592).
	if fixExit == ExitOutage {
		return d.deployFixOutage(ctx, pmKey, comments, report.Summary)
	}
	// The fixer's own blocker evidence rides on the marker: the escalation
	// line says what the fixer was sent to fix, the four fields say what it
	// found (SC-5179). Only the exit that defines a blocker carries one; a
	// needs-input stop may leave the template's placeholders in the object.
	m := failureMarker(MarkerDeployFailed,
		deployFixEscalationReason(fixExit, dispatchedFailure(comments), deployFixRounds(comments)))
	if fixExit == ExitNeedsHumanWork {
		m = report.Blocker.addTo(m)
	}
	_, _ = d.Commenter.AddComment(ctx, pmKey, markerBody(m, "reason", "kind", "evidence", "attempted", "release"))
	return nil
}

// deployFixOutage posts the done stage's outage marker for a fixer that reported
// the substrate was unreachable, and says it once: an identical standing marker is
// left in place rather than re-dated, which is both what keeps the ticket from
// collecting one per tick and what makes the wait measurable (outageRunSince,
// SC-2851). summary is the fixer's own line, so the card names what was
// unreachable instead of the generic fallback.
func (d BoardTransitionDeps) deployFixOutage(ctx context.Context, pmKey string, comments []tracker.Comment, summary string) error {
	// The fixer's summary is agent-authored free text and the template's "one
	// line" is not enforced: collapsed to a single line (not just the first)
	// so a multi-line summary neither duplicates itself into the composed
	// sentence nor lets a "resume:" line ride along and get scanned by
	// parseResumeLine as the marker's own field (SC-5592).
	reason := strings.Join(strings.Fields(summary), " ")
	body := markerBody(pausedOutageMarker(outageTypeFor(BoardDoneStage), nil, "", "", reason))
	if outageAlreadyStated(comments, BoardDoneStage, body) {
		d.Logger.Info().Str("pm", pmKey).
			Msg("board deploy fix: the card already says the substrate is down, not repeating it")
		return nil
	}
	if _, err := d.Commenter.AddComment(ctx, pmKey, body); err != nil {
		return errors.WrapWithDetails(err, "posting the deploy-fix outage marker", "pm", pmKey)
	}
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
func deployFixEscalationReason(fixExit StageExit, dispatched string, rounds int) string {
	blocking := ""
	if dispatched != "" {
		blocking = " The failure it was sent to fix: " + dispatched
	}
	// The round count is appended LAST, after every existing clause, so none of
	// the wording below changes and a failure with no rounds behind it (rounds
	// <= 0) claims none — deployRoundsSentence returns "" in that case (SC-3640).
	attempts := deployRoundsSentence(rounds)
	// Every member is listed on purpose and the linter enforces it: an exit class
	// falling silently into the default below is exactly how SC-5592 shipped —
	// ExitOutage joined the vocabulary and this switch never had to decide. The
	// `default` stays as a runtime guard for a value read off the wire.
	//exhaustive:enforce
	switch fixExit {
	case ExitNeedsInput:
		return "the deploy fixer needs a human decision — read the PR and its CI, decide, then re-run Deploy." + blocking + attempts
	case ExitNeedsHumanWork:
		return "the deploy failure needs manual work the fixer could not do — resolve it on the branch, then re-run Deploy." + blocking + attempts
	case ExitOutage:
		// Not reachable from AdvanceDeployFix, which routes an outage to the
		// uncharged outage marker above before any escalation is composed. Written
		// as a real answer rather than a fall-through so a direct caller — or a
		// future route — states the wait instead of blaming the branch.
		return "the deploy fixer could not reach the substrate it needs — wait for it to come back, then re-run Deploy." + blocking + attempts
	case ExitRetryable, ExitDone:
		return deployFixStalledReason(dispatched, blocking) + attempts
	default:
		return deployFixStalledReason(dispatched, blocking) + attempts
	}
}

// deployFixStalledReason is the headline for a fixer that ran and did not recover:
// it names the blocking failure and refuses to advise a retry that would hit it
// again (SC-3615).
func deployFixStalledReason(dispatched, blocking string) string {
	if dispatched != "" {
		return "the deploy fixer could not recover the deploy. Fix it on the branch and push — " +
			"re-running Deploy alone will hit the same failure." + blocking
	}
	return "the deploy fixer stopped without recovering the deploy — check the PR and its CI, then re-run Deploy"
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
	_ = d.DeployBranch(ctx, req.PMKey, req.PMTitle, doneBody(req.PMKey, card, card.Branch), card.Branch)
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
// head is the commit the freshness rebase PUBLISHED — "" is a no-op, since
// nothing moved and there is nothing to re-gate.
func (d BoardTransitionDeps) regateAfterRebase(ctx context.Context, pmKey string, res PRResult, branch, head string, logger zerolog.Logger) error {
	if head == "" {
		return nil
	}
	logger.Info().Int("pr", res.Number).Str("head", head).Msg("deploy: branch was stale; rebased onto the base, re-gating CI")
	if err := d.waitForChecks(ctx, res, head); err != nil {
		if ciFailureFixable(err) {
			return d.deployFailedOrDispatchFixer(ctx, pmKey, res, ciFailureHeadline(err), err, branch, false)
		}
		return d.deployFailed(pmKey, res.URL, deployReason(ciFailureHeadline(err), err))
	}
	if err := d.awaitMergeable(ctx, res, head); err != nil {
		headline := "the forge still reports the pull request unmergeable after the freshness rebase — open the PR to see why, then re-run Deploy"
		if stateUnreadable(err) {
			headline = "could not read the pull request's mergeability — " + credentialRemedy
		}
		if headLagged(err) {
			headline = headLagHeadline
		}
		return d.deployFailed(pmKey, res.URL, deployReason(headline, err))
	}
	return nil
}

const mergeRefusedHeadline = "the forge refused the merge — open the PR to see why, then re-run Deploy"

// mergeAfterFreshness re-gates what the freshness rebase invalidated and merges,
// re-integrating the base when the forge refuses because the head is out of
// date. head is the commit EnsureMergeable published ("" when nothing moved).
// Two branches that reach the merge within a minute of each other both land
// here: the first moves the base, the second is refused, re-integrates, re-gates
// its new head and merges — instead of redding for a person to re-run (SC-5395).
func (d BoardTransitionDeps) mergeAfterFreshness(ctx context.Context, pmKey string, res PRResult, branch, head string, logger zerolog.Logger) error {
	for round := 0; ; round++ {
		if err := d.regateAfterRebase(ctx, pmKey, res, branch, head, logger); err != nil {
			return err // regateAfterRebase already posted the failure
		}
		err := d.mergeWithRetry(ctx, res.Number)
		if err == nil {
			return nil
		}
		if !isHeadOutOfDate(err) || round >= mergeReintegrations {
			return d.deployFailed(pmKey, res.URL, deployReason(mergeRefusedHeadline, err))
		}
		logger.Info().Int("pr", res.Number).Int("round", round+1).
			Msg("deploy: the forge reports the head out of date; re-integrating the base and re-gating")
		newHead, ensureErr := d.Deployer.EnsureMergeable(ctx, PRRequest{WorkspaceDir: d.WorkspaceDir, Branch: branch})
		if ensureErr != nil {
			return d.deployFailedOrDispatchFixer(ctx, pmKey, res,
				"the branch conflicts with the base — resolve the conflict on "+branch+
					" (rebase it onto the base branch), then re-run Deploy",
				ensureErr, branch, false)
		}
		head = newHead
	}
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
		if d.recordDeployedBestEffort(ctx, pmKey, marker.Marker{
			Type:   MarkerDeployed,
			Fields: fields("merged", "already in the base branch; no new PR opened"),
		}) {
			d.closeTicketBestEffort(pmKey)
		}
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
	if err := d.waitForChecks(ctx, res, ""); err != nil {
		if ciFailureFixable(err) {
			return d.deployFailedOrDispatchFixer(ctx, pmKey, res, ciFailureHeadline(err), err, branch, false)
		}
		return d.deployFailed(pmKey, res.URL, deployReason(ciFailureHeadline(err), err))
	}
	// Freshness stage: own the branch's mergeability BEFORE attempting the merge.
	// When main has advanced past the branch point the forge would reject the
	// merge (GitHub 405) and the card would dead-end; rebasing and re-pushing here
	// turns that terminal failure into a mechanical, human-free recovery. A real
	// conflict surfaces as a loud deploy-failed instead of a blind merge attempt.
	head, ensureErr := d.Deployer.EnsureMergeable(ctx, PRRequest{
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
				ensureErr, branch, false)
		}
	}
	if err := d.mergeAfterFreshness(ctx, pmKey, res, branch, head, logger); err != nil {
		return err
	}
	// Past the merge the work IS shipped: branch cleanup and the ticket close
	// are best-effort and must never turn the card red. Best-effort here means
	// recorded-and-surfaced, not silent: a failed close leaves the card in the
	// board's Fix column (the frontend only drops a card once the ticket leaves
	// the tracker's open list), so the operator must see it and close by hand;
	// an unrecordable merge is likewise surfaced, and the close is withheld so
	// the trail and the status cannot contradict (SC-5594).
	logger.Info().Int("pr", res.Number).Str("url", res.URL).Msg("deploy: merged")
	_ = d.Deployer.DeleteRemoteBranch(ctx, d.WorkspaceDir, branch)
	if d.recordDeployedBestEffort(ctx, pmKey, marker.Marker{
		Type: MarkerDeployed, Fields: fields("pr", res.URL),
	}) {
		d.closeTicketBestEffort(pmKey)
		logger.Info().Msg("deploy: done")
	}
	return nil
}

// recordDeployedBestEffort puts the merge on the ticket and reports whether it
// landed. The merge itself is done and must never red the card, but the record
// is not optional the way the close is: the trail and the status are read
// together, so a close on top of a missing [human:deployed] leaves a card that
// derives as a failed deploy and is hidden as Done, with nothing in the log
// saying so (SC-5594). One immediate retry on its own context recovers both the
// transient tracker blip and a deploy whose 45-minute deadline is already
// spent; a refusal that outlives that is logged, and the caller answers a false
// by leaving the ticket OPEN — the card then stays visible and the next
// re-drive (or reconcileShippedFailures, which sees open cards only) can
// repair it. Mirrors closeTicketBestEffort, whose failure is surfaced the same
// way rather than swallowed.
func (d BoardTransitionDeps) recordDeployedBestEffort(ctx context.Context, pmKey string, m marker.Marker) bool {
	err := postMarker(ctx, d.Commenter, pmKey, m)
	if err != nil {
		postCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = postMarker(postCtx, d.Commenter, pmKey, m)
	}
	if err == nil {
		return true
	}
	d.Logger.Warn().Err(err).Str("pm", pmKey).
		Msg("the merge could not be recorded on the ticket; leaving it open so the card stays visible")
	return false
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

// deployHeadLagDetail tags a deploy error whose cause is that the forge never
// reported the head this deploy published. It is neither a check verdict nor a
// credential failure, and there is nothing in the branch for a code fixer to
// change — so it must route to a plain deploy-failed (SC-5395).
const deployHeadLagDetail = "deployHeadLag"

// headLagged reports whether an error is the forge never catching up to the
// head a freshness rebase published.
func headLagged(err error) bool {
	lagged, _ := errors.AllDetails(err)[deployHeadLagDetail].(bool)
	return lagged
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
	if headLagged(err) {
		return headLagHeadline
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
	if headLagged(err) {
		return false // a forge that never moved its PR resource is not a code defect
	}
	return err != nil && !strings.Contains(err.Error(), "timed out") && !stateUnreadable(err)
}

// PullRequestShippable reports whether state is DEFINITELY ready to ship: the
// forge reports the pull request mergeable and every check it reports has
// passed. Anything less is NOT a no — it is "cannot say", and the done-stage
// recovery that consults this must leave such a card exactly as it is.
//
// An empty check list is deliberately not shippable. Absence is not a verdict:
// a head pushed moments ago reports no checks because CI has not registered
// yet, which is the reading that merged a candidate three seconds ahead of its
// own checks (the ChecksNone rule, internal/forge/forge.go). The deploy gate
// can wait absence out with deployNoChecksGrace; a probe that fires once per
// reconcile tick cannot, so it declines.
func PullRequestShippable(state *forge.PullRequestState) bool {
	if state == nil || !state.Mergeable || len(state.Checks) == 0 {
		return false
	}
	for _, c := range state.Checks {
		if c.Conclusion != forge.ChecksPassing {
			return false
		}
	}
	return true
}

// deployRoundsSentence states how many automated deploy-fixer rounds ran before
// a failure, so "no automation exists for this" and "two rounds ran and gave up"
// are never the same sentence — they were byte-identical, and a card that had
// been worked twice read exactly like one nothing had touched (SC-3640).
//
// Empty for zero: a failure with no rounds behind it must claim none. Appended
// after the existing headline rather than woven into it, so every wording the
// deploy already produces stays byte-for-byte what it was.
func deployRoundsSentence(rounds int) string {
	switch {
	case rounds <= 0:
		return ""
	case rounds == 1:
		return " 1 automated fix round ran before this."
	default:
		return " " + strconv.Itoa(rounds) + " automated fix rounds ran before this."
	}
}

// deployBudgetSpentSentence tells a person what the one gesture the board offers
// will actually DO. With the budget spent, Retry deploy is no longer a repeat of
// the failure just read: it re-arms a single fixer round. Without this sentence
// the card was byte-identical across every retry, so the move that would help
// looked exactly like the move that had just failed (SC-5595).
//
// It names the BOARD's Retry deploy specifically. `human deploy <KEY>` grants
// nothing — the fix skills run that command from inside their own runs, and a
// re-arm there would be the machine funding itself (AD7) — so advising it here
// would advertise a gesture that does not do what the sentence claims.
//
// Appended after deployRoundsSentence rather than woven into the headline, the
// same way and for the same reason: every wording the deploy already produces
// stays byte-for-byte what it was.
func deployBudgetSpentSentence(spent bool) string {
	if !spent {
		return ""
	}
	return " The automated fix budget for this ticket is spent; Retry deploy on the card grants one further fix round."
}

// headLagHeadline is the deploy-failed headline for a forge that never
// reported the head a freshness rebase published — nothing in the branch for
// a code fixer to change, so the card reds plainly instead of dispatching one.
const headLagHeadline = "the forge did not report the rebased head on the pull request — open the PR to see which commit it carries, then re-run Deploy"

// awaitPublishedHead blocks until the forge reports wantHead as the pull
// request's head. wantHead is "" for the pre-rebase gate, where the PR's head
// is whatever the branch already was and there is nothing to wait for.
func (d BoardTransitionDeps) awaitPublishedHead(ctx context.Context, res PRResult, wantHead string) error {
	if wantHead == "" {
		return nil
	}
	deadline := time.Now().Add(deployHeadWaitTimeout)
	var reported string
	for {
		state, err := d.Deployer.ReadPullRequest(ctx, d.WorkspaceDir, res.Number)
		if err != nil {
			return markStateUnreadable(err, "could not read the pull request's head", "pr", res.URL)
		}
		if state != nil {
			reported = strings.TrimSpace(state.HeadSHA)
			if reported == wantHead {
				d.Logger.Info().Int("pr", res.Number).Str("head", wantHead).
					Msg("deploy: the forge reports the head this deploy published")
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.WithDetails(
				"the forge did not report the rebased head on the pull request",
				"pr", res.URL, "published", wantHead, "reported", reported, deployHeadLagDetail, true)
		}
		select {
		case <-ctx.Done():
			// A cancelled context here — the deploy's own timeout expiring inside
			// this wait, or a daemon shutdown — is the same "never caught up"
			// outcome as the local deadline above, not a CI verdict: tag it
			// headLagged so ciFailureFixable never calls it a fixable check
			// failure and dispatches a fixer at a green PR (SC-5395).
			return errors.WrapWithDetails(ctx.Err(),
				"the forge did not report the rebased head on the pull request before the deploy's context ended",
				"pr", res.URL, "published", wantHead, "reported", reported, deployHeadLagDetail, true)
		case <-time.After(headPollInterval):
		}
	}
}

// awaitMergeable waits for the forge's asynchronous mergeability recompute to
// settle after a freshness-rebase re-push. Read errors and a false verdict
// both retry — the recompute window routinely yields either — until the
// timeout, which is the point where "still computing" and "genuinely
// unmergeable" can no longer be told apart. It first waits for the forge to
// report wantHead so the recompute it polls is the one for the head this
// deploy just published, never the tip the rebase replaced (SC-5395).
func (d BoardTransitionDeps) awaitMergeable(ctx context.Context, res PRResult, wantHead string) error {
	if err := d.awaitPublishedHead(ctx, res, wantHead); err != nil {
		return err
	}
	deadline := time.Now().Add(mergeablePollTimeout)
	for {
		mergeable, err := d.Deployer.PullRequestMergeable(ctx, d.WorkspaceDir, res.Number)
		if err == nil && mergeable {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				// A persistent read error is an unreadable state, not a verdict of
				// unmergeable — tag it UNKNOWN so the card reports a credential
				// failure rather than blaming the branch (SC-1996).
				return markStateUnreadable(err, "could not read the pull request's mergeability", "pr", res.URL)
			}
			return errors.WithDetails("forge reports the pull request unmergeable", "pr", res.URL)
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

// isTransientMergeRefusal reports whether a merge refusal is one that clears on
// its own. It classifies on the status the forge answered with — apiclient
// stamps it on the error as "statusCode" — rather than on a rendered message,
// because the set was written as an enumeration of one observed response and a
// second status for the same "the head moved under you" condition (409) fell
// outside it and dead-ended the card (SC-1184, SC-5395).
func isTransientMergeRefusal(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// A draft refusal is also a 405 and is the opposite of transient: nothing
	// about the branch changes while it is retried (SC-4027).
	if strings.Contains(msg, "still a draft") {
		return false
	}
	if code, ok := errors.AllDetails(err)["statusCode"].(int); ok {
		return code == http.StatusMethodNotAllowed || code == http.StatusConflict
	}
	// A refusal the forge reported inside a 200 body carries no status.
	return strings.Contains(msg, "not mergeable") || strings.Contains(msg, "405") ||
		strings.Contains(msg, "out of date")
}

// isHeadOutOfDate distinguishes the one transient refusal that does NOT clear
// by waiting: the base advanced under the gate because a sibling deploy landed.
// Waiting cannot fix it; only re-integrating the base can.
func isHeadOutOfDate(err error) bool {
	if err == nil {
		return false
	}
	if code, ok := errors.AllDetails(err)["statusCode"].(int); ok && code == http.StatusConflict {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "out of date")
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
// wantHead is the commit a freshness rebase published ("" for the pre-rebase
// gate); the wait first blocks until the forge reports it before reading any
// verdict, so a verdict is never read for the tip the rebase replaced
// (SC-5395).
func (d BoardTransitionDeps) waitForChecks(ctx context.Context, res PRResult, wantHead string) error {
	if err := d.awaitPublishedHead(ctx, res, wantHead); err != nil {
		return err
	}
	ticker := time.NewTicker(deployCheckInterval)
	defer ticker.Stop()
	// This wait is the deploy's long silence — minutes with nothing written
	// down, which is what makes an interrupted deploy indistinguishable
	// afterwards from one that never started. A heartbeat every few minutes is
	// enough to tell the two apart without a line per poll.
	d.Logger.Info().Int("pr", res.Number).Msg("deploy: waiting for CI checks")
	polls := 0
	var noneSince time.Time
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
			d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Str("head", wantHead).Msg("deploy: CI checks passed")
			return nil
		case forge.ChecksFailing:
			d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Str("head", wantHead).Msg("deploy: CI checks failed")
			return errors.WithDetails("CI checks failed", "pr", res.URL,
				deployFailingChecksDetail, d.checkNames(res.Number, forge.ChecksFailing))
		case forge.ChecksNone:
			// Absence is pending until it has lasted the grace: the head may be
			// too young for its CI to have registered. Past the grace it is a
			// repository without CI, and the gate has nothing to block on.
			if noneSince.IsZero() {
				noneSince = time.Now()
			}
			if time.Since(noneSince) >= deployNoChecksGrace {
				d.Logger.Info().Int("pr", res.Number).Int("polls", polls).Dur("grace", deployNoChecksGrace).
					Msg("deploy: no CI checks reported within the grace; treating the repository as having no CI")
				return nil
			}
		default:
			// A reported check is evidence the CI is alive: a later absence (a
			// re-push that wiped the head's runs) restarts the grace.
			noneSince = time.Time{}
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
func (d BoardTransitionDeps) deployFailedOrDispatchFixer(ctx context.Context, pmKey string, res PRResult, headline string, cause error, branch string, beforeReview bool) error {
	// The thread is read on BOTH paths now: the count of what was already
	// attempted belongs on the message a person reads, and it used to be
	// available only on the path that dispatched another round. A read that
	// fails leaves rounds at zero, which claims nothing rather than guessing.
	rounds, budgetSpent := 0, false
	if comments, err := d.Commenter.ListComments(ctx, pmKey); err == nil {
		rounds = deployFixRounds(comments)
		if d.Launcher != nil {
			// Two ways a round may start: the ticket's own budget has room, or a
			// person re-ran Deploy and re-armed one. Without the second, every
			// Retry on a spent card found the same conflict, dispatched nobody and
			// reposted an identical failure (SC-5595).
			if rounds < DefaultDeployFixRounds || deployFixGrants(comments) > 0 {
				// rounds travels with the dispatch so the two failure arms INSIDE it
				// — a refused launch gate, a launch that errors — can state what
				// already ran. They are deploy failures that followed rounds too.
				return d.dispatchDeployFixer(ctx, pmKey, res, branch, headline, beforeReview, rounds)
			}
			budgetSpent = rounds >= DefaultDeployFixRounds
		}
	}
	return d.deployFailed(pmKey, res.URL,
		deployReason(headline+deployRoundsSentence(rounds)+deployBudgetSpentSentence(budgetSpent), cause))
}

// dispatchDeployFixer launches the deploy-fixer and, only when one actually
// started, posts the running deploy-fix-started marker (carrying the failure
// headline and the PR binding for the trail). The marker keeps the card spinning
// rather than red while the fixer works — which is only true of a fixer that
// exists, so it follows the launch (SC-4244).
// rounds is what the caller already counted: the deploy-fixer rounds that ran
// before this dispatch. Both failure arms below — a refused launch gate, a
// launch that errors — are deploy failures that FOLLOWED automation, and both
// used to read exactly like a first failure nothing had touched (SC-3640).
func (d BoardTransitionDeps) dispatchDeployFixer(ctx context.Context, pmKey string, res PRResult, branch, headline string, beforeReview bool, rounds int) error {
	launched, err := d.launchDeployFixAgent(ctx, pmKey, deployFixDispatch(pmKey, res.Number, branch), rounds)
	if stderrors.Is(err, ErrLaunchGateRefused) {
		// The deploy DID fail — the fixer is only the remedy — and a host that
		// cannot launch the remedy has no way to record the failure but the
		// failure itself. Swallowing it left the card on a deploy-fix-started
		// marker nothing re-drives (SC-5108, round 4).
		return d.deployFailed(pmKey, res.URL, deployReason(headline+deployRoundsSentence(rounds), err))
	}
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
	order := []string{"pr", "number", "branch"}
	if beforeReview {
		m.Fields[DeployFixBeforeReviewField] = deployFixBeforeReviewValue
		order = append(order, DeployFixBeforeReviewField)
	}
	// Which budget paid for this round. The caller already counted the rounds
	// that ran, and the gate above only reaches here with the budget spent when a
	// grant is standing — so the funding source is derivable and needs no
	// parameter of its own (AD5). Recorded on the marker the round posts, so the
	// grant is consumed by the same durable record that proves the round started:
	// a dispatch that never launched posts nothing and consumes nothing, which is
	// the SC-4244 rule holding for grants as it already does for rounds.
	if grantFunded := rounds >= DefaultDeployFixRounds; grantFunded {
		m.Fields[DeployFixGrantField] = deployFixGrantValue
		order = append(order, DeployFixGrantField)
	}
	if err := postMarker(ctx, d.Commenter, pmKey, m, order...); err != nil {
		return errors.WrapWithDetails(err, "posting deploy-fix-started marker", "pm", pmKey)
	}
	return nil
}

// launchDeployFixAgent launches the deploy-fixer fire-and-forget (no claim: driven
// by this daemon's local Stop event, like the PR-loop agents). A launch failure
// reds the card — leaving it spinning would strand the deploy.
func (d BoardTransitionDeps) launchDeployFixAgent(ctx context.Context, pmKey, prompt string, rounds int) (launched bool, err error) {
	// Launch gate: same refusal as the other two launch paths (SC-5108).
	if d.launchGateBlocked(ctx, pmKey, deployFixAgentStage) {
		return false, errors.WrapWithDetails(ErrLaunchGateRefused, "launch gate refused the deploy fixer", "pm", pmKey)
	}
	name := agentNameFor(pmKey, deployFixAgentStage)
	started, err := d.launchAgent(ctx, pmKey, name, prompt)
	if err != nil {
		body := markerBody(failureMarker(MarkerDeployFailed,
			"could not launch the deploy fixer — "+errors.CauseChain(err)+deployRoundsSentence(rounds)))
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
		" post the terminal marker — human marker post " + key + " nothing-to-do --field \"evidence=<the verdict, and the key that carries the work>\"" +
		" --field reason=<duplicate|escalated|rejected> (duplicate for a superseded verdict, escalated for escalated, rejected for rejected — never merged, that reason is the planner's) —" +
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
// branch is taken as its own parameter rather than read off card.Branch: a
// handoff-less loop (`human deploy --branch`) has no card.Branch, and a caller
// driving the loop's own derived branch (doneStageBranch) would otherwise see it
// silently dropped from the PR body.
func doneBody(pmKey string, card BoardCard, branch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "PM ticket: %s\n", pmKey)
	if card.EngineeringKey != "" {
		fmt.Fprintf(&b, "Engineering ticket: %s\n", card.EngineeringKey)
	}
	if branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", branch)
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
