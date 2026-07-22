// Package cmdhandoff surfaces the review handoff — the [human:ready-for-review]
// marker through which a finished build is handed to a reviewer — as a pair of
// deterministic commands. Every field of the handoff is derivable (branch from
// git, commits from the ticket-key commit search, daemon id from the
// environment), and posting verifies the named commits are actually reachable
// on the branch so a handoff can never bind a reviewer to commits that live
// nowhere but a local checkout.
package cmdhandoff

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/marker"
	"github.com/gethuman-sh/human/internal/tracker"
)

// handoffType is the marker type this command family owns.
const handoffType = "ready-for-review"

// Handoff is the parsed review handoff, as printed by show.
type Handoff struct {
	Engineering []string `json:"engineering,omitempty"`
	Branch      string   `json:"branch"`
	Commits     []string `json:"commits"`
	Daemon      string   `json:"daemon,omitempty"`
}

// BuildHandoffCmd creates the top-level "handoff" command.
func BuildHandoffCmd(deps cmdutil.Deps) *cobra.Command {
	handoffCmd := &cobra.Command{
		Use:   "handoff",
		Short: "Post and read the [human:ready-for-review] handoff on a ticket",
	}
	handoffCmd.AddCommand(buildPostCmd(deps), buildShowCmd(deps))
	return handoffCmd
}

func buildPostCmd(deps cmdutil.Deps) *cobra.Command {
	var engineering, branch, commits, notes string
	var noVerify bool
	cmd := &cobra.Command{
		Use:   "post KEY",
		Short: "Post a verified review handoff (branch/commits derived from git when omitted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			opts := PostOptions{
				Engineering: splitList(engineering),
				Branch:      branch,
				Commits:     splitList(commits),
				Notes:       notes,
				DaemonID:    os.Getenv("HUMAN_DAEMON_ID"),
				Verify:      !noVerify,
			}
			return RunHandoffPost(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), ".", resolved.Key, opts)
		},
	}
	cmd.Flags().StringVar(&engineering, "engineering", "", "Engineering ticket keys, comma-separated (omit in single-tracker topology)")
	cmd.Flags().StringVar(&branch, "branch", "", "Branch the commits live on (default: current branch)")
	cmd.Flags().StringVar(&commits, "commits", "", "Short SHAs, comma-separated (default: commits referencing the work keys)")
	cmd.Flags().StringVar(&notes, "notes", "", "Open items / caveats to record in the handoff body")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "Skip verifying the commits are reachable on the branch")
	return cmd
}

func buildShowCmd(deps cmdutil.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show KEY",
		Short: "Print the newest review handoff on a ticket as parsed JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := cmdutil.ResolveAutoProvider(cmd.Context(), cmd, args[0], true, deps)
			if err != nil {
				return err
			}
			defer resolved.Cleanup()
			return RunHandoffShow(cmd.Context(), resolved.Provider, cmd.OutOrStdout(), resolved.Key)
		},
	}
	return cmd
}

// PostOptions carries the explicit overrides for a handoff post; zero values
// mean "derive it".
type PostOptions struct {
	Engineering []string
	Branch      string
	Commits     []string
	Notes       string
	DaemonID    string
	Verify      bool
}

// RunHandoffPost derives missing fields, verifies commit reachability, and
// posts the handoff marker on the ticket.
func RunHandoffPost(ctx context.Context, p tracker.Provider, out io.Writer, dir, key string, opts PostOptions) error {
	branch := opts.Branch
	if branch == "" {
		derived, err := gitrepo.CurrentBranch(ctx, dir)
		if err != nil {
			return err
		}
		if derived == "HEAD" {
			return errors.WithDetails("detached HEAD has no branch — pass --branch", "dir", dir)
		}
		branch = derived
	}

	commits := opts.Commits
	if len(commits) == 0 {
		derived, err := deriveCommits(ctx, dir, key, branch, opts.Engineering)
		if err != nil {
			return err
		}
		commits = derived
	}
	if len(commits) == 0 {
		return errors.WithDetails("no commits reference the work keys — commit first or pass --commits", "key", key)
	}

	if opts.Verify {
		if err := verifyReachable(ctx, dir, branch, commits); err != nil {
			return err
		}
	}

	fields := map[string]string{
		"branch":  branch,
		"commits": strings.Join(commits, ", "),
	}
	if len(opts.Engineering) > 0 {
		fields["engineering"] = strings.Join(opts.Engineering, ", ")
	}
	if strings.TrimSpace(opts.DaemonID) != "" {
		fields["daemon"] = opts.DaemonID
	}
	m := marker.Marker{Type: handoffType, Fields: fields}
	if strings.TrimSpace(opts.Notes) != "" {
		m.Body = strings.TrimSpace(opts.Notes)
	}
	rendered := marker.Render(m, []string{"engineering", "branch", "commits", "daemon"})
	if _, err := p.AddComment(ctx, key, rendered); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, rendered)
	return err
}

// RunHandoffShow prints the newest handoff on the ticket as parsed JSON.
func RunHandoffShow(ctx context.Context, p tracker.Provider, out io.Writer, key string) error {
	comments, err := p.ListComments(ctx, key)
	if err != nil {
		return err
	}
	m, ok := marker.Latest(comments, handoffType)
	if !ok {
		return errors.WithDetails("no review handoff on ticket", "key", key)
	}
	h := Handoff{
		Engineering: splitList(m.Fields["engineering"]),
		Branch:      m.Fields["branch"],
		Commits:     splitList(m.Fields["commits"]),
		Daemon:      m.Fields["daemon"],
	}
	if h.Commits == nil {
		h.Commits = []string{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(h)
}

// deriveCommits collects the short SHAs referencing the work keys (the
// engineering keys in split topology, the ticket itself otherwise), oldest
// first so the handoff reads in commit order. Discovery is anchored at the
// handed-off BRANCH, not HEAD — in a board workspace the caller's checkout
// usually sits on main while the work lives on the branch (the 1087 deadlock).
func deriveCommits(ctx context.Context, dir, key, branch string, engineering []string) ([]string, error) {
	workKeys := engineering
	if len(workKeys) == 0 {
		workKeys = []string{key}
	}
	var shas []string
	seen := map[string]bool{}
	for _, workKey := range workKeys {
		found, err := gitrepo.CommitsForRev(ctx, dir, workKey, branch)
		if err != nil {
			return nil, err
		}
		// git log lists newest first; a handoff reads oldest first.
		for i := len(found) - 1; i >= 0; i-- {
			sha := found[i].ShortSHA
			if !seen[sha] {
				seen[sha] = true
				shas = append(shas, sha)
			}
		}
	}
	return shas, nil
}

// verifyReachable rejects the handoff when any commit is not reachable on the
// branch as known to origin — the exact failure that once bound reviewers to
// commits living only in a local checkout. The fetch is best-effort: offline,
// the local refs are still checked.
func verifyReachable(ctx context.Context, dir, branch string, commits []string) error {
	_ = gitrepo.Fetch(ctx, dir, branch)
	var unreachable []string
	for _, sha := range commits {
		if !gitrepo.CommitReachable(ctx, dir, branch, sha) {
			unreachable = append(unreachable, sha)
		}
	}
	if len(unreachable) > 0 {
		return errors.WithDetails("commits not reachable on branch — push first or pass --no-verify",
			"branch", branch, "commits", strings.Join(unreachable, ", "))
	}
	return nil
}

// splitList splits a comma-separated list, trimming blanks.
func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
