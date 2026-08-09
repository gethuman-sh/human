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

A githubs: entry with no role used to be both a tracker and the code host that
opened pull requests. It stays as the tracker and gains a forge entry beside it
carrying the same token. An entry that declared role: forge was never a tracker
and is moved into forges: outright.

Vault references are copied as references, never resolved — a token written
into a config file is a credential leak. Running it twice changes nothing.`,
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

	verb := "Wrote"
	if dryRun {
		verb = "Would write"
	}
	for _, name := range result.Added {
		if _, err := fmt.Fprintf(out, "%s forges: entry %q\n", verb, name); err != nil {
			return err
		}
	}
	for _, name := range result.Moved {
		if _, err := fmt.Fprintf(out, "%s githubs: entry %q (role: forge) into forges:\n",
			map[bool]string{true: "Would move", false: "Moved"}[dryRun], name); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "%s: %s\n", map[bool]string{true: "Target", false: "Updated"}[dryRun], result.File)
	return err
}
