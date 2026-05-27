package cmddaemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/chrome"
)

const chromeBridgeChildEnv = "_HUMAN_CHROME_BRIDGE_CHILD"

// BuildChromeBridgeCmd creates the "chrome-bridge" command.
func BuildChromeBridgeCmd(version string) *cobra.Command {
	var foreground bool

	cmd := &cobra.Command{
		Use:   "chrome-bridge",
		Short: "Bridge Chrome MCP socket to daemon (for devcontainer use)",
		Long: `Creates a fake Unix socket that Claude's MCP server can discover,
and tunnels traffic over TCP to the daemon running on the host.

Requires HUMAN_CHROME_ADDR and HUMAN_DAEMON_TOKEN environment variables.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr := os.Getenv("HUMAN_CHROME_ADDR")
			if addr == "" {
				return errors.WithDetails("HUMAN_CHROME_ADDR environment variable is required")
			}

			token := os.Getenv("HUMAN_DAEMON_TOKEN")
			if token == "" {
				return errors.WithDetails("HUMAN_DAEMON_TOKEN environment variable is required")
			}

			out := cmd.OutOrStdout()

			if foreground || os.Getenv(chromeBridgeChildEnv) != "" {
				return runChromeBridgeForeground(addr, token, version, out)
			}
			return runChromeBridgeBackground(addr, token, out)
		},
	}

	cmd.Flags().BoolVar(&foreground, "foreground", false, "Run in foreground (don't daemonize)")

	return cmd
}

// runChromeBridgeForeground runs the bridge in the current process (blocking).
// SIGTERM is trapped in addition to SIGINT so container/systemd/k8s
// shutdowns run the bridge's deferred socket cleanup instead of
// bypassing it under a hard kill.
func runChromeBridgeForeground(addr, token, version string, _ io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	bridge := &chrome.Bridge{
		Dialer:  chrome.DefaultDialer{},
		Addr:    addr,
		Token:   token,
		Version: version,
		Logger:  logger,
	}

	return bridge.ListenAndServe(ctx)
}

// runChromeBridgeBackground re-execs the current binary as a detached child process.
func runChromeBridgeBackground(addr, token string, out io.Writer) error {
	logPath := ChromeBridgeLogPath()

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- logPath is built by ChromeBridgeLogPath(), not user input
	if err != nil {
		return errors.WrapWithDetails(err, "opening log file", "path", logPath)
	}

	exe, err := os.Executable()
	if err != nil {
		_ = logFile.Close()
		return errors.WrapWithDetails(err, "resolving executable path")
	}

	child := exec.Command(exe, "chrome-bridge", "--foreground") // #nosec G204 -- re-exec of own binary via os.Executable()
	child.Env = append(os.Environ(), chromeBridgeChildEnv+"=1")
	child.Stderr = logFile
	child.Stdout = logFile
	child.SysProcAttr = detachSysProcAttr()

	if err := child.Start(); err != nil {
		_ = logFile.Close()
		return errors.WrapWithDetails(err, "starting background process")
	}
	_ = logFile.Close()

	pid := child.Process.Pid

	// Detach so we don't wait for the child.
	_ = child.Process.Release()

	socketDir, err := chrome.SocketDir()
	if err != nil {
		return errors.WrapWithDetails(err, "resolving socket directory")
	}
	sockPath := filepath.Join(socketDir, fmt.Sprintf("%d.sock", pid))

	// Poll for socket to appear (up to 2s).
	const (
		pollInterval = 50 * time.Millisecond
		pollTimeout  = 2 * time.Second
	)
	deadline := time.Now().Add(pollTimeout)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			ready = true
			break
		}
		time.Sleep(pollInterval)
	}

	if !ready {
		_, _ = fmt.Fprintf(out, "Chrome bridge started (PID %d) but socket not yet ready\n", pid)
		_, _ = fmt.Fprintf(out, "  Log: %s\n", logPath)
		return nil
	}

	_, _ = fmt.Fprintf(out, "Chrome bridge started (PID %d)\n", pid)
	_, _ = fmt.Fprintf(out, "  Socket: %s\n", sockPath)
	_, _ = fmt.Fprintf(out, "  Log:    %s\n", logPath)
	return nil
}

// ChromeBridgeLogPath returns the path to the chrome bridge log file
// (~/.human/chrome-bridge.log), creating the directory if needed.
func ChromeBridgeLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "chrome-bridge.log")
	}
	dir := filepath.Join(home, ".human")
	_ = os.MkdirAll(dir, 0o750)
	return filepath.Join(dir, "chrome-bridge.log")
}
