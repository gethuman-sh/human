package daemon

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	client "github.com/gethuman-sh/human-daemon-client"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// AgentLauncher launches a containerized agent for a board stage. It is an
// interface so the transition engine is testable without Docker.
type AgentLauncher interface {
	Launch(ctx context.Context, name, prompt, workspace, configDir string) error
}

// PRPublisher pushes the recorded branch and opens a pull request. Injected so
// the Done stage is testable without git/forge access.
type PRPublisher interface {
	PushAndCreatePR(ctx context.Context, req PRRequest) (prURL string, err error)
}

// PRRequest carries everything needed to push a branch and open its PR.
type PRRequest struct {
	WorkspaceDir string
	Branch       string
	Title        string
	Body         string
}

// BoardTransitionRequest is the wire request for advancing a card one stage.
// PMTitle is carried from the card so the Done stage can title the PR without a
// second tracker fetch. The struct is defined by the public human-daemon-client
// contract; the daemon aliases it.
type BoardTransitionRequest = client.BoardTransitionRequest

// BoardTransitionDeps wires the transition engine's collaborators.
type BoardTransitionDeps struct {
	Commenter    tracker.Commenter
	Launcher     AgentLauncher
	Publisher    PRPublisher
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
	comments, err := d.Commenter.ListComments(ctx, req.PMKey)
	if err != nil {
		return errors.WrapWithDetails(err, "loading PM comments for transition", "pm", req.PMKey)
	}
	card := DeriveBoardCard(comments, tracker.CategoryUnstarted)

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
		if card.EngineeringKey == "" {
			return errors.WithDetails("no engineering key for implementation", "pm", req.PMKey)
		}
		return d.startAgentStage(ctx, req.PMKey, BoardImplementation, ImplementationStartedHeader,
			"/human-execute "+card.EngineeringKey)
	case BoardVerification:
		if card.EngineeringKey == "" {
			return errors.WithDetails("no engineering key for verification", "pm", req.PMKey)
		}
		return d.startAgentStage(ctx, req.PMKey, BoardVerification, ReviewStartedHeader,
			"/human-review "+card.EngineeringKey)
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

// runDoneStage pushes the recorded branch and opens a PR. A missing branch or a
// push/PR failure posts [human:pr-failed] with the error rather than leaving a
// partial PR; success posts [human:pr-pushed] with the PR URL.
func (d BoardTransitionDeps) runDoneStage(ctx context.Context, req BoardTransitionRequest, card BoardCard) error {
	if card.Branch == "" {
		body := PRFailedHeader + "\nno branch recorded on ready-for-review handoff"
		_, _ = d.Commenter.AddComment(ctx, req.PMKey, body)
		return errors.WithDetails("no branch recorded for Done", "pm", req.PMKey)
	}
	if _, err := d.Commenter.AddComment(ctx, req.PMKey, PRStartedHeader); err != nil {
		return errors.WrapWithDetails(err, "posting pr-started marker", "pm", req.PMKey)
	}
	url, err := d.Publisher.PushAndCreatePR(ctx, PRRequest{
		WorkspaceDir: d.WorkspaceDir,
		Branch:       card.Branch,
		Title:        req.PMTitle,
		Body:         doneBody(req.PMKey, card),
	})
	if err != nil {
		body := PRFailedHeader + "\n" + err.Error()
		_, _ = d.Commenter.AddComment(ctx, req.PMKey, body)
		return errors.WrapWithDetails(err, "pushing and creating PR", "pm", req.PMKey)
	}
	if _, err := d.Commenter.AddComment(ctx, req.PMKey, PRPushedHeader+"\npr: "+url); err != nil {
		return errors.WrapWithDetails(err, "posting pr-pushed marker", "pm", req.PMKey)
	}
	return nil
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
		return PRFailedHeader
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
