package cmdusage

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/daemon"
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
	if err := printUsage(w, instances, localTranscriptRoots(), now); err != nil {
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

func printUsage(w io.Writer, instances []claude.Instance, roots []string, now time.Time) error {
	if len(instances) == 0 {
		return printLocalUsage(w, roots, now)
	}
	return printInstanceUsage(w, instances, now)
}

// printLocalUsage reports the current window across every transcript root on
// this host, not just the operator's own — the agents' spend is the larger half
// on a machine that runs the pipeline (SC-3581).
func printLocalUsage(w io.Writer, roots []string, now time.Time) error {
	summary := claude.CalculateUsageRoots(claude.OSDirWalker{}, roots, now)
	return claude.FormatUsage(w, summary, now)
}

// localTranscriptRoots enumerates the transcript roots this command can see.
// `usage` never forwards to the daemon (main.go localSubcommands), so it has no
// ProjectRegistry: the registered project dirs are read from the daemon info
// file, which records them and is readable whether or not a daemon is running.
// The working directory is offered too, so the command is still complete on a
// machine where a daemon was never started; TranscriptRoots de-duplicates the
// overlap.
func localTranscriptRoots() []string {
	dirs := registeredProjectDirs()
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		dirs = append(dirs, cwd)
	}
	return claude.TranscriptRoots(dirs)
}

// registeredProjectDirs reads the project dirs the daemon registered. A missing
// or malformed daemon info file yields none rather than an error: the operator
// tree alone is a smaller answer, not a broken one.
func registeredProjectDirs() []string {
	info, err := daemon.ReadInfo()
	if err != nil {
		return nil
	}
	dirs := make([]string, 0, len(info.Projects))
	for _, p := range info.Projects {
		if p.Dir != "" {
			dirs = append(dirs, p.Dir)
		}
	}
	return dirs
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
