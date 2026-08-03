// Package cmdunderway surfaces the live "is this ticket already being built"
// check as `human underway KEY`: the open pull requests and branches that
// reference the ticket key on the workspace forge. Preflight consults it before
// a run starts so it never duplicates open work, and never blocks on a stale
// ticket that merely overlaps in wording (SC-2648).
package cmdunderway

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/cmd/cmddaemon"
	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/forge"
	"github.com/gethuman-sh/human/internal/tracker"
	"github.com/gethuman-sh/human/internal/vault"
)

// underwayResult is the JSON the command emits.
type underwayResult struct {
	Key      string           `json:"key"`
	Repo     string           `json:"repo"`
	Underway bool             `json:"underway"`
	Work     []forge.OpenWork `json:"work"`
}

// findOpenWork is the seam to the daemon's forge resolution; a package var so
// tests exercise output shaping without a real forge.
var findOpenWork = func(ctx context.Context, key string) ([]forge.OpenWork, string, error) {
	vcfg, err := vault.ReadConfig(".")
	if err != nil {
		vcfg = nil // fall back to env tokens, mirroring cmddeploy
	}
	return cmddaemon.FindOpenWorkForKey(ctx, ".", os.LookupEnv, vault.NewResolverFromConfig(vcfg), key)
}

// commitKindFor resolves the owning tracker kind for a key from the
// configured instances, so a bare numeric key is canonicalized with the
// "SC-" prefix only on a Shortcut workspace (SC-2855). A package var so tests
// inject the workspace's trackers without real config.
var commitKindFor = func(_ context.Context, key string) string {
	vcfg, err := vault.ReadConfig(".")
	if err != nil {
		vcfg = nil
	}
	instances, _ := cmdutil.LoadAllInstancesTolerant(".", os.LookupEnv, vault.NewResolverFromConfig(vcfg))
	return tracker.CommitKind(key, instances)
}

// BuildUnderwayCmd creates the top-level "underway" command.
func BuildUnderwayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "underway KEY",
		Short: "Report open PRs and branches already referencing a ticket key (the live 'already being built' signal)",
		Long: `Report the pull requests and branches already open against a ticket on the
workspace forge. An empty "work" list means nothing is open for this ticket —
a run may start. A non-empty list means work is already underway: preflight
stops and names it rather than building a second copy.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return RunUnderway(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
}

// RunUnderway resolves the workspace forge and prints the open work for key.
func RunUnderway(ctx context.Context, out io.Writer, key string) error {
	canonical := tracker.CanonicalCommitKey(key, commitKindFor(ctx, key))
	work, repo, err := findOpenWork(ctx, canonical)
	if err != nil {
		return err
	}
	if work == nil {
		work = []forge.OpenWork{}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(underwayResult{
		Key: canonical, Repo: repo, Underway: len(work) > 0, Work: work,
	})
}
