package daemon

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
)

// AgentLauncher launches a containerized agent for a board stage. It is an
// interface so the transition engine is testable without Docker.
type AgentLauncher interface {
	Launch(ctx context.Context, name, prompt, workspace, configDir string) error
}

// Deployer executes the forge side of the deploy pipeline: push + PR, the CI
// gate, the merge, and branch cleanup. Injected so the Done stage is testable
// without git/forge access.
type Deployer interface {
	PushAndCreatePR(ctx context.Context, req PRRequest) (PRResult, error)
	PullRequestChecks(ctx context.Context, workspaceDir string, number int) (forge.ChecksState, error)
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
}

// PRRequest carries everything needed to push a branch and open its PR.
type PRRequest struct {
	WorkspaceDir string
	Branch       string
	Title        string
	Body         string
}

// PRResult identifies the created pull request for the pipeline steps that
// follow creation (checks, merge).
type PRResult struct {
	URL    string
	Number int
}

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
)

// BoardTransitionRequest is the wire request for advancing a card one stage.
// PMTitle is carried from the card so the Done stage can title the PR without a
// second tracker fetch.
type BoardTransitionRequest struct {
	PMKey   string     `json:"pm_key"`
	PMTitle string     `json:"pm_title"`
	From    BoardStage `json:"from"`
	To      BoardStage `json:"to"`
}

// BoardTransitionDeps wires the transition engine's collaborators.
type BoardTransitionDeps struct {
	Commenter tracker.Commenter
	Launcher  AgentLauncher
	Deployer  Deployer
	// CloseTicket closes the PM ticket after a successful deploy so shipped
	// work leaves the board. nil skips the close (the deploy still succeeds).
	CloseTicket  func(pmKey string) error
	WorkspaceDir string
	ConfigDir    string
	// DaemonID stamps this daemon's identity on every marker it posts. Empty
	// leaves markers un-stamped (StampDaemon no-ops), so an un-provisioned
	// daemon still functions.
	DaemonID string
	// Logger records best-effort post-merge failures (e.g. a failed automated
	// close). The zero value is a safe no-op writer, so an un-wired path stays
	// valid without a logger.
	Logger zerolog.Logger
	// LaunchGate reports the launch-critical doctor checks currently failing on
	// this daemon's host (docker, agent-skills, claude-auth). When it returns a
	// non-empty slice the stage launcher neither claims nor launches — it silently
	// leaves the work for a healthy daemon, and the failure surfaces only on this
	// host (doctor / rail LED), never as a ticket marker (SC-912). nil disables.
	LaunchGate func(ctx context.Context) []DoctorCheck
}

// sanitizeRe drops characters that are invalid in an agent name (alphanumeric,
// hyphen, underscore only) so a PM key like "SC-105" maps to a valid,
// reversible agent name.
var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitize(s string) string {
	return sanitizeRe.ReplaceAllString(s, "-")
}

// agentNameFor builds the agent name for a board stage. It is reversible (see
// parseAgentName) so the failure watcher can recover (pmKey, stage) from a
// SessionEnd event.
func agentNameFor(pmKey string, stage BoardStage) string {
	return "board-" + sanitize(pmKey) + "-" + string(stage)
}

// parseAgentName recovers the PM key and stage from a board agent name. The PM
// key is returned sanitized (the form embedded in the name), which is
// sufficient to re-resolve comments since the daemon fetched the same keys.
func parseAgentName(name string) (pmKey string, stage BoardStage, ok bool) {
	rest, found := strings.CutPrefix(name, "board-")
	if !found {
		return "", "", false
	}
	idx := strings.LastIndex(rest, "-")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], BoardStage(rest[idx+1:]), true
}

// ApplyTransition advances a card from its current stage to the requested next
// stage. The daemon re-loads live comments and re-derives the card here because
// the UI gate is advisory only — the daemon is the authority on whether a
// forward move is allowed (forward-only, single-step, gated on the prior
// stage's completion). All errors carry details for the client.
func (d BoardTransitionDeps) ApplyTransition(ctx context.Context, req BoardTransitionRequest) error {
	// Ideas never move via board transitions: promotion out of the Ideas
	// column is a label swap performed by the ideation engine's evolve mode,
	// which the desktop opens instead of calling this route.
	if req.From == BoardIdeas || req.To == BoardIdeas {
		return errors.WithDetails("ideas transitions are handled via ideation",
			"pm", req.PMKey, "from", string(req.From), "to", string(req.To))
	}

	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM comments for transition", "pm", req.PMKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted, false)

	// Idempotency: if the target stage already has an open *-started marker, a
	// duplicate drop (e.g. a quick re-drag before the board refetches) must not
	// launch a second agent or re-post the marker. Checked first because a
	// re-drop derives the card as already sitting in the target stage, which
	// the forward-only rule below would otherwise reject as a non-advance.
	if _, state := latestStageState(comments, req.To); state == BoardRunning {
		return nil
	}

	// Rework loop: a build whose review failed may be rebuilt. This is the ONE
	// sanctioned backward move — the executor is re-dispatched with the review
	// findings, and the resulting handoff chains into a fresh review.
	if isReworkTransition(req.To, card) {
		return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			"/human-execute "+dispatchKey(req.PMKey, card)+
				" — a review found problems; address the findings in the latest [human:review-complete] comment on the ticket first")
	}

	// Planning retry: a failed planning run is relaunched in place. The retry
	// gesture targets planning while the card already derives to planning, so
	// the single-step rule below would reject it and the gesture would launch
	// nothing (SC-355). A RUNNING planning card never reaches this path — the
	// idempotency guard above already returned for it.
	if isPlanningRetry(req.To, card) {
		return d.startAgentStage(ctx, req.PMKey, BoardPlanning, PlanningStartedHeader,
			"/human-plan "+req.PMKey)
	}

	// Build retry: the same sanctioned in-place relaunch for a failed
	// implementation run — without it a failed build is a dead end, since the
	// rework re-drop requires a failed REVIEW verdict and Retry fix is
	// bug-pane-only (SC-591). The plan is intact on the ticket; a fresh
	// executor picks it up.
	if isBuildRetry(req.To, card) {
		return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			"/human-execute "+dispatchKey(req.PMKey, card))
	}

	// Review retry: a stage-failed review is otherwise a dead end. The rework
	// re-drop keys on a DONE verification with a failing verdict, and a
	// [human:review-failed] card (state failed) matches neither it nor any
	// forward move — so a failed binding gate (missing branch, unreachable
	// commits) could never be retried. Relaunch the review in place, re-bound to
	// the same handoff (SC-695). A RUNNING review is caught by the idempotency
	// guard above.
	if isReviewRetry(req.To, card) {
		return d.startAgentStage(ctx, req.PMKey, BoardVerification, ReviewStartedHeader,
			reviewPrompt(dispatchKey(req.PMKey, card), card))
	}

	// Deploy retry: a card sitting on a failed deploy, re-dropped on Deploy, must
	// re-run the deploy pipeline — the freshness stage rebases the already-reviewed
	// branch and re-attempts the merge. Without this the forward-only rule below
	// rejects the same-stage move and a conflicted deploy is a dead end that can
	// only be escaped by re-implementing already-reviewed work (735).
	if isDeployRetry(req.To, card) {
		return d.runDoneStage(ctx, req, card)
	}

	// Forward-only, single-next-stage: the target must be exactly one rank
	// above the current derived stage.
	if stageRank[req.To] != stageRank[card.Stage]+1 {
		return errors.WithDetails("transition is not the single next stage",
			"pm", req.PMKey, "current", string(card.Stage), "to", string(req.To))
	}

	// Gating: every boundary except Backlog→Planning requires the prior stage
	// to have completed (done-marker present).
	if card.Stage != BoardBacklog && card.State != BoardDone {
		return errors.WithDetails("prior stage not complete",
			"pm", req.PMKey, "stage", string(card.Stage), "state", string(card.State))
	}

	// A failing review verdict blocks the deploy: the card must be rebuilt
	// (rework loop) and re-reviewed before it can ship.
	if req.To == BoardDoneStage && VerdictFailed(card.Verdict) {
		return errors.WithDetails("review verdict blocks deploy",
			"pm", req.PMKey, "verdict", card.Verdict)
	}

	return d.launchForwardStage(ctx, req, card)
}

// launchForwardStage dispatches an already-sanctioned forward transition to
// its stage launcher. Split from ApplyTransition so the gate chain and the
// dispatch read (and count) as separate concerns.
func (d BoardTransitionDeps) launchForwardStage(ctx context.Context, req BoardTransitionRequest, card BoardCard) error {
	switch req.To {
	case BoardPlanning:
		return d.startAgentStage(ctx, req.PMKey, BoardPlanning, PlanningStartedHeader,
			"/human-plan "+req.PMKey)
	case BoardImplementation:
		return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			"/human-execute "+dispatchKey(req.PMKey, card))
	case BoardVerification:
		return d.startAgentStage(ctx, req.PMKey, BoardVerification, ReviewStartedHeader,
			reviewPrompt(dispatchKey(req.PMKey, card), card))
	case BoardDoneStage:
		return d.runDoneStage(ctx, req, card)
	default:
		return errors.WithDetails("unsupported transition target", "to", string(req.To))
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
	// whole-card guard (SC-230). latestStageState mirrors ApplyTransition's
	// duplicate-drop guard and is immune to that masking.
	if _, state := latestStageState(comments, BoardImplementation); state == BoardRunning {
		return nil
	}
	if _, state := latestStageState(comments, BoardVerification); state == BoardRunning {
		return nil
	}
	// The --board marker is the mechanical gate that keeps a board run from
	// pushing: the container holds no push/PR credentials, and the daemon's
	// Deploy stage owns push → PR → CI → merge on the host against the
	// bind-mounted repo. The skill and fixer branch on this flag to stop at the
	// review handoff. Relying on the HUMAN_AGENT_NAME env var alone let a fixer
	// push and fail — the fix completed and passed review but the card ended red
	// (SC-252).
	return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
		"/human-autofix "+req.PMKey+" --board")
}

// startAgentStage posts the stage's started marker, then launches the agent. On
// launch failure it posts the stage's *-failed marker so the board reflects the
// error rather than leaving a stuck spinner.
func (d BoardTransitionDeps) startAgentStage(ctx context.Context, pmKey string, stage BoardStage, startedHeader, prompt string) error {
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
			return nil
		}
	}
	// Claim before start: with several daemons on one board, arbitrate who
	// launches this stage so the work is picked up exactly once (SC-660 rule 2).
	// A loser backs off silently — not an error — leaving the started marker and
	// the launch to the winning daemon.
	won, err := d.winClaim(ctx, pmKey, stage)
	if err != nil {
		return err
	}
	if !won {
		return nil
	}
	if _, err := d.Commenter.AddComment(ctx, pmKey, StampDaemon(startedHeader, d.DaemonID)); err != nil {
		return errors.WrapWithDetails(err, "posting started marker", "pm", pmKey, "stage", string(stage))
	}
	name := agentNameFor(pmKey, stage)
	if err := d.Launcher.Launch(ctx, name, prompt, d.WorkspaceDir, d.ConfigDir); err != nil {
		failBody := failedHeaderFor(stage) + "\n" + errors.CauseChain(err)
		_, _ = d.Commenter.AddComment(ctx, pmKey, StampDaemon(failBody, d.DaemonID))
		return errors.WrapWithDetails(err, "launching agent", "pm", pmKey, "stage", string(stage))
	}
	return nil
}

// startDeploy launches the deploy pipeline in the background. A package var so
// tests can run the pipeline synchronously.
var startDeploy = func(d BoardTransitionDeps, req BoardTransitionRequest, card BoardCard) {
	go d.deploy(context.Background(), req, card)
}

// runDoneStage kicks off the deploy pipeline: push → PR → CI gate → merge →
// branch cleanup → close ticket. The CI gate can take many minutes, so the
// transition request returns as soon as [human:deploy-started] is posted and
// the pipeline reports the outcome via markers.
func (d BoardTransitionDeps) runDoneStage(ctx context.Context, req BoardTransitionRequest, card BoardCard) error {
	if card.Branch == "" {
		body := DeployFailedHeader + "\nno branch recorded on ready-for-review handoff"
		_, _ = d.Commenter.AddComment(ctx, req.PMKey, StampDaemon(body, d.DaemonID))
		return errors.WithDetails("no branch recorded for deploy", "pm", req.PMKey)
	}
	if _, err := d.Commenter.AddComment(ctx, req.PMKey, StampDaemon(DeployStartedHeader, d.DaemonID)); err != nil {
		return errors.WrapWithDetails(err, "posting deploy-started marker", "pm", req.PMKey)
	}
	startDeploy(d, req, card)
	return nil
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

// DeployBranch runs the deterministic deploy gate for pmKey's branch: the
// already-merged short-circuit, push + PR, the CI gate, the freshness rebase,
// the merge, branch cleanup, markers, and the ticket close. Failures are both
// posted as deploy-failed markers (the board's channel) and returned (the CLI's
// channel).
func (d BoardTransitionDeps) DeployBranch(ctx context.Context, pmKey, title, prBody, branch string) error {
	deployGate.Lock()
	defer deployGate.Unlock()
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	// Already-merged carve-out: a re-run Deploy on a card whose branch is already
	// on the base has nothing to ship. Opening a PR would draw the forge's 422
	// "No commits between" and red a card that is genuinely finished — so
	// short-circuit to the terminal success path (deployed/done, ticket closed).
	// This mirrors the "already done, stop cleanly" carve-outs Planning and
	// Implementation already carry (SC-911).
	if d.Deployer.BranchMerged(ctx, d.WorkspaceDir, branch) {
		_, _ = d.Commenter.AddComment(ctx, pmKey,
			StampDaemon(DeployedHeader+"\nalready merged into the base branch; no new PR opened", d.DaemonID))
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
		return d.deployFailed(pmKey, "", deployReason(
			"could not push "+branch+" and open its pull request — check the branch and forge access, then re-run Deploy",
			err))
	}
	if err := d.waitForChecks(ctx, res); err != nil {
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
		if !d.forgeMergeableFallback(ctx, res) {
			return d.deployFailed(pmKey, res.URL, deployReason(
				"the branch conflicts with the base — resolve the conflict on "+branch+" (rebase it onto the base branch), then re-run Deploy",
				ensureErr))
		}
	}
	if rebased {
		// The re-push invalidated the forge's cached mergeability; merging
		// before the recompute settles draws a spurious 405 on a clean branch.
		if err := d.awaitMergeable(ctx, res.Number); err != nil {
			return d.deployFailed(pmKey, res.URL, deployReason(
				"the forge still reports the pull request unmergeable after the freshness rebase — open the PR to see why, then re-run Deploy",
				err))
		}
	}
	if err := d.Deployer.MergePullRequest(ctx, d.WorkspaceDir, res.Number); err != nil {
		return d.deployFailed(pmKey, res.URL, deployReason(
			"the forge refused the merge — open the PR to see why, then re-run Deploy",
			err))
	}
	// Past the merge the work IS shipped: branch cleanup and the ticket close
	// are best-effort and must never turn the card red. Best-effort here means
	// recorded-and-surfaced, not silent: a failed close leaves the card in the
	// board's Fix column (the frontend only drops a card once the ticket leaves
	// the tracker's open list), so the operator must see it and close by hand.
	_ = d.Deployer.DeleteRemoteBranch(ctx, d.WorkspaceDir, branch)
	_, _ = d.Commenter.AddComment(ctx, pmKey, StampDaemon(DeployedHeader+"\npr: "+res.URL, d.DaemonID))
	d.closeTicketBestEffort(pmKey)
	return nil
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

// ciFailureHeadline maps the CI gate's two failure modes to their next step.
func ciFailureHeadline(err error) string {
	if strings.Contains(err.Error(), "timed out") {
		return "CI did not finish within the deploy window — check the PR's checks, then re-run Deploy"
	}
	return "CI checks failed on the pull request — fix the failing checks, then re-run Deploy"
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
				return err
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

// forgeMergeableFallback reports whether the deploy may proceed to the merge
// despite a failed mechanical rebase: true only when the forge reports the PR
// mergeable AND CI is green on the tip. Any read error is treated as "do not
// proceed" so the card reds rather than merging on an unknown state (SC-804).
func (d BoardTransitionDeps) forgeMergeableFallback(ctx context.Context, res PRResult) bool {
	mergeable, err := d.Deployer.PullRequestMergeable(ctx, d.WorkspaceDir, res.Number)
	if err != nil || !mergeable {
		return false
	}
	state, err := d.Deployer.PullRequestChecks(ctx, d.WorkspaceDir, res.Number)
	return err == nil && state == forge.ChecksPassing
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
	body := CloseFailedHeader + "\ndeployed, but the automated close of " + pmKey +
		" failed: " + errors.CauseChain(err) + "\nclose this ticket manually to clear the card."
	_, _ = d.Commenter.AddComment(postCtx, pmKey, StampDaemon(body, d.DaemonID))
}

// waitForChecks blocks until the PR's CI verdict is conclusive. Passing
// returns nil; failing and a gate timeout return an error carrying the reason.
func (d BoardTransitionDeps) waitForChecks(ctx context.Context, res PRResult) error {
	ticker := time.NewTicker(deployCheckInterval)
	defer ticker.Stop()
	for {
		state, err := d.Deployer.PullRequestChecks(ctx, d.WorkspaceDir, res.Number)
		if err != nil {
			return err
		}
		switch state {
		case forge.ChecksPassing:
			return nil
		case forge.ChecksFailing:
			return errors.WithDetails("CI checks failed", "pr", res.URL)
		}
		select {
		case <-ctx.Done():
			return errors.WithDetails("timed out waiting for CI checks", "pr", res.URL)
		case <-ticker.C:
		}
	}
}

// deployFailed posts the failure marker on its own context: the pipeline's
// context may already be cancelled (timeout), and the marker must still land.
func (d BoardTransitionDeps) deployFailed(pmKey, prURL, reason string) error {
	postCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := DeployFailedHeader + "\n" + reason
	if prURL != "" {
		body += "\npr: " + prURL
	}
	_, _ = d.Commenter.AddComment(postCtx, pmKey, StampDaemon(body, d.DaemonID))
	return errors.WithDetails("deploy failed: "+reason, "pm", pmKey, "pr", prURL)
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
	return to == BoardVerification &&
		card.Stage == BoardVerification &&
		card.State == BoardFailed
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
// planning on a card whose planning run failed. Failed-state only — a running
// planning card is protected by ApplyTransition's idempotency guard (SC-355).
func isPlanningRetry(to BoardStage, card BoardCard) bool {
	return to == BoardPlanning &&
		card.Stage == BoardPlanning &&
		card.State == BoardFailed
}

// isBuildRetry mirrors isPlanningRetry for the implementation stage: failed
// builds only — running builds are protected by the idempotency guard, and a
// verification-stage card takes the rework path instead (SC-591).
func isBuildRetry(to BoardStage, card BoardCard) bool {
	return to == BoardImplementation &&
		card.Stage == BoardImplementation &&
		card.State == BoardFailed
}

// isDeployRetry reports the deploy-stage twin of isBuildRetry: relaunching the
// deploy pipeline on a card whose deploy failed. Failed-state only — a running
// deploy is protected by ApplyTransition's idempotency guard. The retry rebases
// and re-deploys the already-reviewed branch rather than re-implementing it, so
// a conflicted deploy is never a dead end (735).
func isDeployRetry(to BoardStage, card BoardCard) bool {
	return to == BoardDoneStage &&
		card.Stage == BoardDoneStage &&
		card.State == BoardFailed
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

// failedHeaderFor returns the *-failed marker header for a stage.
func failedHeaderFor(stage BoardStage) string {
	switch stage {
	case BoardPlanning:
		return PlanningFailedHeader
	case BoardImplementation:
		return ImplementationFailedHeader
	case BoardVerification:
		return ReviewFailedHeader
	case BoardDoneStage:
		return DeployFailedHeader
	default:
		return ""
	}
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
		if !haveLatest || c.Created.After(latest.Created) {
			latest = c
			haveLatest = true
			state = s
		}
	}
	return haveLatest, state
}
