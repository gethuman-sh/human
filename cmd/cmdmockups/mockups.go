// Package cmdmockups surfaces the read-only mockup lookups planning and
// execution agents need. Its only child today, `chosen`, prints the absolute
// path of a ticket's chosen winner mockup so those agents inherit the
// human-selected design direction without any manual copying.
package cmdmockups

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/mockups"
)

// BuildMockupsCmd builds `human mockups ...`. Today its only child is `chosen`,
// a read-only lookup used by planning/execution agents to inherit the winner.
func BuildMockupsCmd() *cobra.Command {
	mockupsCmd := &cobra.Command{
		Use:   "mockups",
		Short: "Local mockup lookups for planning and execution agents",
	}

	chosenCmd := &cobra.Command{
		Use:   "chosen KEY",
		Short: "Print the absolute path of the ticket's chosen winner mockup (nothing if none)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return RunMockupsChosen(cmd.OutOrStdout(), cwd, args[0])
		},
	}

	mockupsCmd.AddCommand(chosenCmd)
	return mockupsCmd
}

// RunMockupsChosen prints the absolute path of the ticket's chosen mockup HTML
// when a winner is recorded AND the file exists on disk. Absence is never an
// error — an unset winner, a missing store, or a stale choice whose file was
// pruned all print nothing and exit 0, so a caller can branch on empty output.
func RunMockupsChosen(out io.Writer, projectDir, key string) error {
	store := mockups.NewStore(mockups.PathIn(projectDir))
	choice, ok := store.ChosenFor(key)
	if !ok {
		return nil
	}
	path := filepath.Join(projectDir, "mockups", choice.Slug, choice.File)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	_, err := fmt.Fprintln(out, path)
	return err
}
