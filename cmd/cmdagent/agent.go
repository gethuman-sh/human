// Package cmdagent provides cobra commands for managing container-based
// Claude Code agents.
package cmdagent

import (
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

func isTerminal(fd uintptr) bool {
	return term.IsTerminal(int(fd)) // #nosec G115 -- fd is from os.Stdin.Fd(), safe range
}

// dockerExecFlag returns "-it" if stdin is a TTY, "-i" otherwise.
func dockerExecFlag() string {
	if isTerminal(os.Stdin.Fd()) {
		return "-it"
	}
	return "-i"
}

// BuildAgentCmd returns the parent "agent" command with start/stop/list/attach subcommands.
func BuildAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage container-based Claude Code agents",
		Long: `Start, stop, list, and attach to Claude Code agents running in devcontainers.

Each agent runs in its own Docker container with full tool isolation.
The container image is built once (with devcontainer features) and cached.`,
	}

	cmd.AddCommand(buildStartCmd())
	cmd.AddCommand(buildStopCmd())
	cmd.AddCommand(buildListCmd())
	cmd.AddCommand(buildAttachCmd())
	return cmd
}

func newManager(cmd *cobra.Command) (*agent.Manager, func(), error) {
	docker, err := devcontainer.NewDockerClient()
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "connecting to Docker")
	}
	cleanup := func() { _ = docker.Close() }

	return &agent.Manager{
		Docker: docker,
	}, cleanup, nil
}

func buildStartCmd() *cobra.Command {
	var prompt string
	var model string
	var skipPerms bool
	var interactive bool
	var configDir string
	var workspace string
	var rebuild bool

	cmd := &cobra.Command{
		Use:   "start NAME",
		Short: "Start a new Claude Code agent in a container",
		Long: `Create a devcontainer and run Claude Code inside it.

The container image is built from .devcontainer/devcontainer.json on first use,
then cached. Subsequent agents start in seconds.

Use --interactive for a foreground TTY session (you sit at Claude).
Use --prompt to run Claude with a task in the background.

Examples:
  human agent start fix-bug --prompt "/human-plan HUM-42"
  human agent start dev --interactive
  human agent start review --prompt "/human-review HUM-42" --model opus`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			mgr, cleanup, err := newManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			opts := agent.StartOpts{
				Name:        name,
				Prompt:      prompt,
				Model:       model,
				SkipPerms:   skipPerms,
				Interactive: interactive,
				ConfigDir:   configDir,
				Workspace:   workspace,
				Rebuild:     rebuild,
			}

			meta, err := mgr.Start(cmd.Context(), opts)
			if err != nil {
				return err
			}

			// Interactive mode: exec into the container with Claude.
			if interactive {
				dockerPath, lookErr := exec.LookPath("docker")
				if lookErr != nil {
					return errors.WithDetails("docker not found in PATH")
				}
				claudeFlags := mgr.BuildClaudeArgs(opts)
				dockerArgs := []string{"docker", "exec", dockerExecFlag(), "-e", "HUMAN_AGENT_NAME=" + name}
				if meta.RemoteUser != "" {
					dockerArgs = append(dockerArgs, "--user", meta.RemoteUser)
				}
				dockerArgs = append(dockerArgs, meta.ContainerName, "claude")
				dockerArgs = append(dockerArgs, claudeFlags...)
				return syscallExec(dockerPath, dockerArgs, os.Environ())
			}

			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Agent %q started (container: %s)\n", meta.Name, meta.ContainerName)
			_, _ = fmt.Fprintf(out, "Attach:   human agent attach %s\n", meta.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&prompt, "prompt", "", "Task for Claude (e.g. /human-plan HUM-42)")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "Foreground TTY mode (you sit at Claude)")
	cmd.Flags().StringVar(&configDir, "configdir", "", "Directory with .devcontainer/devcontainer.json (default: cwd)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Directory to mount into container (default: cwd)")
	cmd.Flags().StringVar(&model, "model", "", "Claude model to use")
	cmd.Flags().BoolVar(&skipPerms, "skip-permissions", false, "Run with --dangerously-skip-permissions")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "Force image rebuild")
	return cmd
}

func buildStopCmd() *cobra.Command {
	var async bool
	cmd := &cobra.Command{
		Use:   "stop NAME",
		Short: "Stop and remove an agent's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if async {
				// Signal the daemon to clean up asynchronously.
				if info, infoErr := daemon.ReadInfo(); infoErr == nil && info.IsReachable() {
					_, _ = daemon.RunRemote(info.Addr, info.Token, []string{"agent-stop-async", args[0]}, "")
					return nil
				}
				// No daemon: fall through to synchronous stop.
			}
			mgr, cleanup, err := newManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			if err := mgr.Stop(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Agent %q stopped\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&async, "async", false, "Signal daemon to stop agent in background and return immediately")
	return cmd
}

func buildListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, cleanup, err := newManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			_ = mgr.Refresh(cmd.Context())

			metas, err := agent.ListMetas()
			if err != nil {
				return err
			}
			if len(metas) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No agents found.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tCONTAINER\tIMAGE\tAGE")
			for _, m := range metas {
				age := agent.FormatDuration(time.Since(m.CreatedAt))
				ctr := m.ContainerName
				if ctr == "" {
					ctr = "-"
				}
				img := m.ImageName
				if img == "" {
					img = "-"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.Name, m.Status, ctr, img, age)
			}
			return w.Flush()
		},
	}
}

func buildAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach NAME",
		Short: "Attach to a running agent's container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, cleanup, err := newManager(cmd)
			if err != nil {
				return err
			}
			defer cleanup()

			meta, err := mgr.Attach(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			dockerPath, lookErr := exec.LookPath("docker")
			if lookErr != nil {
				return errors.WithDetails("docker not found in PATH")
			}

			dockerArgs := []string{"docker", "exec", dockerExecFlag()}
			if meta.RemoteUser != "" {
				dockerArgs = append(dockerArgs, "--user", meta.RemoteUser)
			}
			dockerArgs = append(dockerArgs, meta.ContainerName, "bash")
			return syscallExec(dockerPath, dockerArgs, os.Environ())
		},
	}
}
