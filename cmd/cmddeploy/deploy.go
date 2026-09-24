// Package cmddeploy surfaces the board's deploy gate as "human deploy KEY":
// push + PR, the machine pull-request review, the CI gate, the freshness
// rebase, the merge, deploy markers, and the ticket close — one deterministic
// sequence agents previously walked through by prose with raw gh commands. The
// engine is the daemon's DeployBranch, so a CLI deploy and a board deploy
// cannot drift apart; that includes the already-merged short-circuit (SC-911)
// that turns a re-run on shipped work into a clean success instead of a 422.
// The command runs the pre-deploy prelude — the open-decision refusal, the
// verdict refusal and the [human:deploy-started] record — through
// internal/daemon's StartDeploy, the same entry point a board route would use
// (SC-3852), and from there enters the review→fix loop the board's Deploy drop
// enters, so a branch that never touched the board is still reviewed before it
// merges (F10). Forwarded to the daemon, the command borrows the daemon's own
// engine wiring from its context; run bare, it has no launcher and says so.
package cmddeploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdauto"
	"github.com/gethuman-sh/human/cmd/cmddaemon"
	"github.com/gethuman-sh/human/cmd/cmdforward"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// BuildDeployCmd creates the top-level "deploy" command.
func BuildDeployCmd(deps cmdutil.Deps) *cobra.Command {
	var branch, title, candidates string
	var ready, overrideDecision bool
	cmd := &cobra.Command{
		Use:   "deploy KEY",
		Short: "Ship a ticket's branch: PR, machine review, CI gate, rebase if stale, merge, markers, ticket close",
		Long: `Run the deploy gate for a ticket's finished branch.

The branch defaults to the ticket's newest [human:ready-for-review] handoff;
the PR title defaults to the ticket title. The branch is pushed and its pull
request opened in draft, and the machine review→fix loop takes it from there:
the loop un-drafts and merges the PR when the reviewer approves, so this
command returns once the review has started. An approval the loop already
recorded for exactly this head is reused rather than re-run. A branch already
merged into the base is a clean success (marker posted, ticket closed), not an
error. A ticket paused on an open [human:options] decision is refused rather
than shipped; --override-decision ships anyway. A blocking review verdict is
refused until the change is rebuilt and re-reviewed. --ready skips the machine
review: it un-drafts the pull request and runs the CI gate and merge in this
command, which then blocks until checks conclude; the start marker records
that the review was skipped.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			return RunDeploy(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key, branch, title, ready, overrideDecision, splitList(candidates))
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "Branch to ship (default: the ticket's review-handoff branch)")
	cmd.Flags().StringVar(&title, "title", "", "PR title (default: the ticket title)")
	cmd.Flags().BoolVar(&ready, "ready", false, "Ship without waiting for the machine review: un-draft the pull request and merge once CI is green")
	cmd.Flags().BoolVar(&overrideDecision, "override-decision", false,
		"Deploy even while an open [human:options] decision is waiting on a person")
	// The forwarding client fills this in from the caller's own checkout
	// (cmdforward): the branches carrying the ticket's commits, which the
	// daemon's checkout cannot see on the caller's behalf (SC-5330). Hidden
	// because it is the client's to set, not a person's.
	cmd.Flags().StringVar(&candidates, strings.TrimPrefix(cmdforward.CandidateBranchesFlag, "--"), "", "")
	_ = cmd.Flags().MarkHidden(strings.TrimPrefix(cmdforward.CandidateBranchesFlag, "--"))
	return cmd
}

// deployEntry is the seam to the daemon's pre-deploy entry point — the prelude
// (open-decision refusal, verdict refusal, start marker) and the review loop or
// engine behind it. A package var so tests exercise derivation without a forge;
// it deliberately points at StartDeploy rather than DeployBranch, because the
// prelude is the thing this route used to skip (SC-3852).
var deployEntry = func(ctx context.Context, d daemon.BoardTransitionDeps, req daemon.StartDeployRequest) (daemon.StartDeployResult, error) {
	return d.StartDeploy(ctx, req)
}

// newTransitionDeps builds the bare-CLI wiring: forge access and the ticket
// close, no launcher. It is what a run with no daemon gets, and what a forwarded
// run replaces with the daemon's resolver (transitionDepsFor). A package var
// for tests.
var newTransitionDeps = func(p tracker.Provider) daemon.BoardTransitionDeps {
	vcfg, err := vault.ReadConfig(".")
	if err != nil {
		// Without a readable vault config the forge client falls back to env
		// tokens; the deploy still proceeds and fails loudly if unresolved.
		vcfg = nil
	}
	return daemon.BoardTransitionDeps{
		Commenter: p,
		Deployer:  cmddaemon.NewForgeDeployer(vault.NewResolverFromConfig(vcfg), os.LookupEnv),
		// The gate's own progress, on stderr: forwarded to the daemon this is the
		// daemon log (the only place a deploy that was interrupted mid-CI leaves a
		// trace), and run locally it is the terminal — which is what a command
		// that can legitimately sit fifteen minutes on a CI gate owes its caller.
		Logger: zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger(),
		CloseTicket: func(pmKey string) error {
			return cmdauto.RunSemanticTransition(context.Background(), p, io.Discard, pmKey, tracker.CategoryDone, false)
		},
		WorkspaceDir: ".",
		DaemonID:     os.Getenv("HUMAN_DAEMON_ID"),
	}
}

// transitionDepsFor prefers the daemon's own engine wiring when the command is
// running inside one: that wiring carries the launcher the machine review
// needs, the signed commenter and the launch gate, none of which the bare CLI
// can build. A daemon that cannot resolve the key's project falls back to the
// bare wiring rather than failing the command on a routing detail.
func transitionDepsFor(ctx context.Context, p tracker.Provider, key string) daemon.BoardTransitionDeps {
	if resolve := daemon.TransitionDepsFromContext(ctx); resolve != nil {
		if deps, err := resolve(key); err == nil {
			return deps
		}
	}
	return newTransitionDeps(p)
}

// RunDeploy derives branch and title, then runs the deploy gate. candidates
// are the branches the caller's checkout found carrying the ticket's commits;
// they stand in for a missing handoff and for nothing else.
func RunDeploy(ctx context.Context, p tracker.Provider, out io.Writer, key, branch, title string, ready, overrideDecision bool, candidates []string) error {
	engineering := ""
	if branch == "" {
		derived, eng, err := branchFromHandoff(ctx, p, key, candidates)
		if err != nil {
			return err
		}
		branch, engineering = derived, eng
	}
	if title == "" {
		issue, err := p.GetIssue(ctx, key)
		if err != nil {
			return err
		}
		title = issue.Title
	}

	deps := transitionDepsFor(ctx, p, key)
	// Only an explicit gesture may clear the review loop's draft interlock; the
	// engine refuses a draft otherwise and says what is holding it (SC-4027).
	deps.MergeDraftPR = ready
	res, err := deployEntry(ctx, deps, daemon.StartDeployRequest{
		PMKey:            key,
		Title:            title,
		PRBody:           prBody(key, engineering, branch),
		Branch:           branch,
		OverrideDecision: overrideDecision,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(out, outcomeLine(key, branch, res))
	return err
}

// branchFromHandoff reads the branch off the ticket's newest review handoff,
// and without one falls back to the single branch the caller found carrying
// the ticket's commits. Two candidates, or none, is refused with what was
// found: the fallback exists so a pushed branch is not refused for want of a
// handoff (SC-5330), not so the gate guesses between branches.
func branchFromHandoff(ctx context.Context, p tracker.Provider, key string, candidates []string) (branch, engineering string, err error) {
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return "", "", err
	}
	m, ok := marker.Latest(comments, "ready-for-review")
	if !ok {
		return branchFromCandidates(key, candidates)
	}
	if m.Fields["branch"] == "" {
		return "", "", errors.WithDetails("review handoff carries no branch — pass --branch", "key", key)
	}
	return m.Fields["branch"], m.Fields["engineering"], nil
}

func branchFromCandidates(key string, candidates []string) (branch, engineering string, err error) {
	switch len(candidates) {
	case 1:
		return candidates[0], "", nil
	case 0:
		return "", "", errors.WithDetails("no review handoff on ticket and no branch carries its commits — pass --branch", "key", key)
	default:
		return "", "", errors.WithDetails("no review handoff on ticket and its commits are on several branches — pass --branch",
			"key", key, "branches", strings.Join(candidates, ", "))
	}
}

// splitList splits a comma-separated list, trimming blanks.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// outcomeLine says where the work is now, because the outcomes leave it in
// different places: merged, held in a draft the machine review will merge, or
// held for a mechanical fixer to resolve a stale base before any review runs.
// Reporting "Deployed" for the others, or "Review started" for the fixer
// dispatch, would be the record misstating the outcome (SC-5279).
func outcomeLine(key, branch string, res daemon.StartDeployResult) string {
	switch res.Outcome {
	case daemon.DeployOutcomeReviewStarted:
		return fmt.Sprintf("Review started for %s (%s): %s — the machine review loop merges it on approval\n", key, branch, res.PRURL)
	case daemon.DeployOutcomeFixDispatched:
		return fmt.Sprintf("Base merge needs a fixer for %s (%s): %s — the deploy fixer resolves it before the machine review runs\n", key, branch, res.PRURL)
	default:
		return fmt.Sprintf("Deployed %s (%s)\n", key, branch)
	}
}

// prBody builds the PR description with the PM→engineering→branch trail,
// mirroring the board's doneBody.
func prBody(pmKey, engineering, branch string) string {
	body := "PM ticket: " + pmKey + "\n"
	if engineering != "" {
		body += "Engineering ticket: " + engineering + "\n"
	}
	body += "Branch: " + branch + "\n"
	return body
}
