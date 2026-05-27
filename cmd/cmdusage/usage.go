package cmdusage

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/claude"
)

// BuildUsageCmd creates the "usage" command.
func BuildUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Show Claude Code token usage for the current 5-hour window",
		RunE: func(cmd *cobra.Command, _ []string) error {
			finder := buildFinder()
			return RunUsage(cmd, finder, time.Now())
		},
	}
}

// RunUsage executes the usage command logic.
func RunUsage(cmd *cobra.Command, finder claude.InstanceFinder, now time.Time) error {
	w := cmd.OutOrStdout()

	instances, _ := finder.FindInstances(cmd.Context())
	if err := printUsage(w, instances, now); err != nil {
		return err
	}

	// Extract container IDs from discovered instances so tmux pane detection
	// can also match panes running "docker exec" into those containers.
	var containerIDs []string
	for _, inst := range instances {
		if inst.Source == "container" && inst.ContainerID != "" {
			containerIDs = append(containerIDs, inst.ContainerID)
		}
	}

	// Append tmux pane listing (best-effort, never fails the command).
	runner := claude.OSCommandRunner{}
	tmuxClient := &claude.OSTmuxClient{Runner: runner}
	procLister := &claude.OSProcessLister{Runner: runner}
	panes, tmuxErr := claude.FindClaudePanes(cmd.Context(), tmuxClient, procLister, containerIDs)
	if tmuxErr == nil && len(panes) > 0 {
		_ = claude.FormatTmuxPanes(w, panes)
	}

	return nil
}

func printUsage(w io.Writer, instances []claude.Instance, now time.Time) error {
	if len(instances) == 0 {
		return printLocalUsage(w, now)
	}
	return printInstanceUsage(w, instances, now)
}

func printLocalUsage(w io.Writer, now time.Time) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".claude", "projects")
	summary, err := claude.CalculateUsage(claude.OSDirWalker{}, root, now)
	if err != nil {
		return err
	}
	return claude.FormatUsage(w, summary, now)
}

func printInstanceUsage(w io.Writer, instances []claude.Instance, now time.Time) error {
	results := claude.CollectInstanceUsage(instances, now)

	switch {
	case len(results) == 0:
		return claude.FormatUsage(w, &claude.UsageSummary{Models: map[string]*claude.ModelUsage{}}, now)
	case len(results) == 1:
		return claude.FormatUsage(w, results[0].Summary, now)
	default:
		return claude.FormatMultiUsage(w, results, now)
	}
}

func buildFinder() claude.InstanceFinder {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Debug().Err(err).Msg("cannot resolve home dir for host finder")
		home = ""
	}

	finders := []claude.InstanceFinder{
		&claude.HostFinder{Runner: claude.OSCommandRunner{}, HomeDir: home},
	}
	if dc, dcErr := claude.NewEngineDockerClient(); dcErr == nil {
		finders = append(finders, &claude.DockerFinder{Client: dc})
	}
	return &claude.CombinedFinder{Finders: finders}
}
