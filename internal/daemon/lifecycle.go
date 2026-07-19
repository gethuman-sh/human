package daemon

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Runner abstracts external process execution so daemon lifecycle helpers
// (StopIfRunning, StartForProject) are unit-testable without spawning a
// real `human` process. execRunner is the production implementation.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() // #nosec G204 -- name is a CLI path resolved by ResolveCLIPath, args are fixed subcommand literals
}

// DefaultRunner is the production Runner (real subprocesses).
var DefaultRunner Runner = execRunner{}

// cliTimeout bounds a single `human daemon start`/`stop` subprocess call.
const cliTimeout = 10 * time.Second

// LookPathFunc matches exec.LookPath's signature so callers can inject a
// fake resolver in tests instead of touching the real PATH.
type LookPathFunc func(file string) (string, error)

// ResolveCLIPath locates the `human` CLI binary via lookup (normally
// exec.LookPath). A re-exec'd daemon start/stop must go through the full
// CLI build — the desktop binary itself does not embed the tracker/tool
// command tree the daemon's CmdFactory needs, only the daemon client.
func ResolveCLIPath(lookup LookPathFunc) (string, error) {
	path, err := lookup("human")
	if err != nil {
		return "", errors.WrapWithDetails(err, "human CLI not found on PATH — install it to manage project daemons")
	}
	return path, nil
}

// StopIfRunning stops the daemon identified by the local PID file, if one is
// alive. No-op (nil error) when nothing is running, so callers can call it
// unconditionally before starting a different project's daemon.
func StopIfRunning(runner Runner, cliPath string) error {
	if _, alive := ReadAlivePid(); !alive {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	if _, err := runner.Run(ctx, cliPath, "daemon", "stop"); err != nil {
		return wrapCLIError(err, "stopping daemon")
	}
	return nil
}

// StartForProject launches a background daemon scoped to dir (via `<cliPath>
// daemon start --project dir`) and polls until it is reachable or timeout
// elapses. Callers must validate dir (config.HasConfigFile) first.
func StartForProject(runner Runner, cliPath, dir string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	if _, err := runner.Run(ctx, cliPath, "daemon", "start", "--project", dir); err != nil {
		return wrapCLIError(err, "starting daemon", "dir", dir)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := ReadInfo(); err == nil && info.IsReachable() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.WithDetails("daemon did not become reachable", "dir", dir, "timeout", timeout.String())
}

// wrapCLIError surfaces the subprocess's stderr (via *exec.ExitError, as
// .Output() stashes it) so a bare "exit status 1" becomes an actionable
// diagnostic — matching internal/vault/ghcli.go's Resolve.
func wrapCLIError(err error, message string, details ...interface{}) error {
	if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
		details = append(details, "stderr", strings.TrimSpace(string(exitErr.Stderr)))
	}
	return errors.WrapWithDetails(err, message, details...)
}
