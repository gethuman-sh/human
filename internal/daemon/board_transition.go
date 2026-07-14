package daemon

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	MergePullRequest(ctx context.Context, workspaceDir string, number int) error
	DeleteRemoteBranch(ctx context.Context, workspaceDir, branch string) error
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

	switch req.To {
	case BoardPlanning:
		return d.startAgentStage(ctx, req.PMKey, BoardPlanning, PlanningStartedHeader,
			"/human-plan "+req.PMKey)
	case BoardImplementation:
		// Single-tracker topology has no separate engineering ticket — the
		// plan lives in a [human:plan] comment on the PM ticket, so the agent
		// is dispatched on the PM key itself.
		key := card.EngineeringKey
		if key == "" {
			key = req.PMKey
		}
		return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			"/human-execute "+key)
	case BoardVerification:
		key := card.EngineeringKey
		if key == "" {
			key = req.PMKey
		}
		return d.startAgentStage(ctx, req.PMKey, BoardVerification, ReviewStartedHeader,
			"/human-review "+key)
	case BoardDoneStage:
		return d.runDoneStage(ctx, req, card)
	default:
		return errors.WithDetails("unsupported transition target", "to", string(req.To))
	}
}

// startAgentStage posts the stage's started marker, then launches the agent. On
// launch failure it posts the stage's *-failed marker so the board reflects the
// error rather than leaving a stuck spinner.
func (d BoardTransitionDeps) startAgentStage(ctx context.Context, pmKey string, stage BoardStage, startedHeader, prompt string) error {
	if _, err := d.Commenter.AddComment(ctx, pmKey, startedHeader); err != nil {
		return errors.WrapWithDetails(err, "posting started marker", "pm", pmKey, "stage", string(stage))
	}
	name := agentNameFor(pmKey, stage)
	if err := d.Launcher.Launch(ctx, name, prompt, d.WorkspaceDir, d.ConfigDir); err != nil {
		failBody := failedHeaderFor(stage) + "\n" + err.Error()
		_, _ = d.Commenter.AddComment(ctx, pmKey, failBody)
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
		_, _ = d.Commenter.AddComment(ctx, req.PMKey, body)
		return errors.WithDetails("no branch recorded for deploy", "pm", req.PMKey)
	}
	if _, err := d.Commenter.AddComment(ctx, req.PMKey, DeployStartedHeader); err != nil {
		return errors.WrapWithDetails(err, "posting deploy-started marker", "pm", req.PMKey)
	}
	startDeploy(d, req, card)
	return nil
}

// deploy walks the pipeline to its end. It runs detached from the transition
// request (whose context dies with the connection), bounded by deployTimeout.
func (d BoardTransitionDeps) deploy(ctx context.Context, req BoardTransitionRequest, card BoardCard) {
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	res, err := d.Deployer.PushAndCreatePR(ctx, PRRequest{
		WorkspaceDir: d.WorkspaceDir,
		Branch:       card.Branch,
		Title:        req.PMTitle,
		Body:         doneBody(req.PMKey, card),
	})
	if err != nil {
		d.deployFailed(req.PMKey, "", err.Error())
		return
	}
	if err := d.waitForChecks(ctx, res); err != nil {
		d.deployFailed(req.PMKey, res.URL, err.Error())
		return
	}
	if err := d.Deployer.MergePullRequest(ctx, d.WorkspaceDir, res.Number); err != nil {
		d.deployFailed(req.PMKey, res.URL, err.Error())
		return
	}
	// Past the merge the work IS shipped: branch cleanup and the ticket close
	// are best-effort and must never turn the card red.
	_ = d.Deployer.DeleteRemoteBranch(ctx, d.WorkspaceDir, card.Branch)
	_, _ = d.Commenter.AddComment(ctx, req.PMKey, DeployedHeader+"\npr: "+res.URL)
	if d.CloseTicket != nil {
		_ = d.CloseTicket(req.PMKey)
	}
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
func (d BoardTransitionDeps) deployFailed(pmKey, prURL, reason string) {
	postCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := DeployFailedHeader + "\n" + reason
	if prURL != "" {
		body += "\npr: " + prURL
	}
	_, _ = d.Commenter.AddComment(postCtx, pmKey, body)
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
