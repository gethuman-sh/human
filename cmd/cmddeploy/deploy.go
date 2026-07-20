// Package cmddeploy surfaces the board's deploy gate as "human deploy KEY":
// push + PR, the CI gate, the freshness rebase, the merge, deploy markers, and
// the ticket close — one deterministic sequence agents previously walked
// through by prose with raw gh commands. The engine is the daemon's
// DeployBranch, so a CLI deploy and a board deploy cannot drift apart; that
// includes the already-merged short-circuit (SC-911) that turns a re-run on
// shipped work into a clean success instead of a 422.
package cmddeploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdauto"
	"github.com/gethuman-sh/human/cmd/cmddaemon"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// BuildDeployCmd creates the top-level "deploy" command.
func BuildDeployCmd(deps cmdutil.Deps) *cobra.Command {
	var branch, title string
	cmd := &cobra.Command{
		Use:   "deploy KEY",
		Short: "Ship a ticket's branch: PR, CI gate, rebase if stale, merge, markers, ticket close",
		Long: `Run the deterministic deploy gate for a ticket's finished branch.

The branch defaults to the ticket's newest [human:ready-for-review] handoff;
the PR title defaults to the ticket title. A branch already merged into the
base is a clean success (marker posted, ticket closed), not an error. The CI
gate blocks until checks conclude, so this command can run for many minutes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			return RunDeploy(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key, branch, title)
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "Branch to ship (default: the ticket's review-handoff branch)")
	cmd.Flags().StringVar(&title, "title", "", "PR title (default: the ticket title)")
	return cmd
}

// deployEngine is the seam to the daemon's deploy implementation; a package
// var so tests exercise derivation without a forge.
var deployEngine = func(ctx context.Context, d daemon.BoardTransitionDeps, pmKey, title, prBody, branch string) error {
	return d.DeployBranch(ctx, pmKey, title, prBody, branch)
}

// newTransitionDeps builds the production wiring; a package var for tests.
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
		CloseTicket: func(pmKey string) error {
			return cmdauto.RunSemanticTransition(context.Background(), p, io.Discard, pmKey, tracker.CategoryDone, false)
		},
		WorkspaceDir: ".",
		DaemonID:     os.Getenv("HUMAN_DAEMON_ID"),
	}
}

// RunDeploy derives branch and title, then runs the deploy gate.
func RunDeploy(ctx context.Context, p tracker.Provider, out io.Writer, key, branch, title string) error {
	engineering := ""
	if branch == "" {
		comments, err := p.ListComments(ctx, key)
		if err != nil {
			return err
		}
		m, ok := marker.Latest(comments, "ready-for-review")
		if !ok {
			return errors.WithDetails("no review handoff on ticket — pass --branch", "key", key)
		}
		branch = m.Fields["branch"]
		engineering = m.Fields["engineering"]
		if branch == "" {
			return errors.WithDetails("review handoff carries no branch — pass --branch", "key", key)
		}
	}
	if title == "" {
		issue, err := p.GetIssue(ctx, key)
		if err != nil {
			return err
		}
		title = issue.Title
	}

	if err := deployEngine(ctx, newTransitionDeps(p), key, title, prBody(key, engineering, branch), branch); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "Deployed %s (%s)\n", key, branch)
	return err
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
