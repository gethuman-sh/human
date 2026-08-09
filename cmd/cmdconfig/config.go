// Package cmdconfig holds commands that operate on .humanconfig itself.
package cmdconfig

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/settings"
)

// BuildConfigCmd creates the "config" command with its migrate subcommand.
func BuildConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Work on .humanconfig itself",
	}

	var dryRun bool
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
twice changes nothing.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunMigrate(cmd.OutOrStdout(), ".", dryRun)
		},
	}
	migrateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change without writing")

	configCmd.AddCommand(migrateCmd)
	return configCmd
}

// RunMigrate performs the forge migration and reports what it did.
func RunMigrate(out io.Writer, dir string, dryRun bool) error {
	result, err := settings.MigrateForges(dir, dryRun)
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
			"If GitHub is where your issues live, add a githubs: entry with role: pm — a GitHub tracker has always needed it to reach the board."); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "%s: %s\n", target, result.File)
	return err
}
