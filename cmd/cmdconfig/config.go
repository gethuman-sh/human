// Package cmdconfig holds commands that operate on .humanconfig itself.
package cmdconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/settings"
)

// BuildConfigCmd creates the "config" command with its migrate subcommand.
func BuildConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Work on .humanconfig itself",
	}

	var dryRun, unify bool
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Bring .humanconfig up to date with the current config shape",
		Long: `Write the forges: section a config needs now that a githubs: entry is an
issue tracker and nothing more.

A githubs: entry that declared no role — or the retired role: forge — held
credentials for the code host, so it moves into forges:. If GitHub is genuinely
where your issues live, declare that with role: pm on a githubs: entry, which is
what the board has always required of a GitHub tracker anyway.

Only name, kind, url and token travel; projects, role and the rest are tracker
concepts. Comments come with the entry, vault references stay references — a
token written into a config file is a credential leak — and an entry whose
credentials cannot be carried is left alone rather than deleted. Running it
twice changes nothing.

--group additionally folds the per-vendor sections into a single trackers: list
where each entry names its kind, the shape forges: already has. It is opt-in
because it changes a working file for the sake of how it reads: both shapes are
read, and will be. Nobody should have to rewrite a config to keep it working.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunMigrate(cmd.OutOrStdout(), ".", dryRun, unify)
		},
	}
	migrateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change without writing")
	migrateCmd.Flags().BoolVar(&unify, "group", false,
		"Also fold the per-vendor sections (githubs:, jiras:, …) into one trackers: list with a kind: field")

	var checkJSON bool
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Check .humanconfig against itself",
		Long: `Report what is wrong with this configuration, from the file alone.

Two kinds of answer. An error means the configuration will not do what it says —
a command fails, or a backend is silently absent. A warning means it works and
costs something its author is unlikely to have meant, which is the class no
loader can catch: whether a config LOADS is a different question from what it
will DO once loaded.

Nothing here touches a secret store or a network. Whether a token resolves is
diagnosed by the commands that need it, at the moment they need it.

Exits non-zero when there is an error, so it can gate a script.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunCheck(cmd.OutOrStdout(), ".", checkJSON)
		},
		// The problems have already been printed in full; a cobra error line
		// would repeat one of them as though it were the only one. The exit code
		// is what carries the verdict onwards.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	checkCmd.Flags().BoolVar(&checkJSON, "json", false, "Output problems as JSON")

	configCmd.AddCommand(migrateCmd, checkCmd)
	return configCmd
}

// RunCheck validates the configuration in dir and prints what it found.
//
// A clean config says so rather than printing nothing: silence from a checker
// is indistinguishable from a checker that did not run.
func RunCheck(out io.Writer, dir string, asJSON bool) error {
	doc, err := config.Load(dir)
	if err != nil {
		return err
	}
	problems := doc.Validate()

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if problems == nil {
			problems = []config.Problem{}
		}
		if err := enc.Encode(problems); err != nil {
			return err
		}
		return exitCode(problems)
	}

	if len(problems) == 0 {
		if _, err := fmt.Fprintf(out, "%s: nothing to report.\n", doc.Path()); err != nil {
			return err
		}
		return nil
	}
	for _, p := range problems {
		where := p.Section
		if p.Instance != "" {
			where += " " + p.Instance
		}
		if _, err := fmt.Fprintf(out, "%s  [%s] %s\n    %s\n", p.Severity, where, p.Rule, p.Message); err != nil {
			return err
		}
		if p.Fix != "" {
			if _, err := fmt.Fprintf(out, "    fix: %s\n", p.Fix); err != nil {
				return err
			}
		}
	}
	return exitCode(problems)
}

// exitCode turns errors into a non-zero exit without printing a second time:
// the problems have already been reported in full, and a cobra error would
// repeat one of them as though it were the only one.
func exitCode(problems []config.Problem) error {
	if config.Errors(problems) {
		return errSilentFailure
	}
	return nil
}

// errSilentFailure carries a non-zero exit with nothing left to say.
var errSilentFailure = errors.WithDetails("configuration has errors")

// RunMigrate performs the migration and reports what it did.
func RunMigrate(out io.Writer, dir string, dryRun, unify bool) error {
	run := settings.MigrateForges
	if unify {
		run = settings.MigrateAll
	}
	result, err := run(dir, dryRun)
	if err != nil {
		return err
	}
	if result.Empty() {
		_, err := fmt.Fprintln(out, "Nothing to migrate — no githubs: entry is acting as a code host.")
		return err
	}

	verb, target := "Moved", "Updated"
	if dryRun {
		verb, target = "Would move", "Target"
	}
	for _, name := range result.Moved {
		if _, err := fmt.Fprintf(out, "%s githubs: entry %q into forges:\n", verb, name); err != nil {
			return err
		}
	}
	// Named explicitly, because this is the one thing the migration cannot know:
	// an entry that declared nothing looked exactly like credentials, and if it
	// was in fact someone's issue tracker they need the line that says so.
	if len(result.Moved) > 0 {
		if _, err := fmt.Fprintln(out,
			"If GitHub is where your issues live, add a githubs: entry for it — a lone tracker reaches the board on any backend, and role: pm is needed only to pick between several."); err != nil {
			return err
		}
	}
	if len(result.Unified) > 0 {
		verb := "Folded"
		if dryRun {
			verb = "Would fold"
		}
		if _, err := fmt.Fprintf(out, "%s %d entries into one trackers: list (%s)\n",
			verb, len(result.Unified), strings.Join(unique(result.Unified), ", ")); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "%s: %s\n", target, result.File)
	return err
}

// unique keeps the first occurrence of each kind, so a config with four Jira
// projects reports jira once rather than four times.
func unique(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}
