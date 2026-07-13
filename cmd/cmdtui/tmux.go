package cmdtui

import (
	"os/exec"

	"github.com/gethuman-sh/human/internal/platform"
)

// pathLooker abstracts exec.LookPath so tmux detection is unit-testable
// without touching the host PATH. Mirrors internal/init.PathLooker but kept
// local to avoid importing the heavy init wizard package into the TUI.
type pathLooker interface {
	LookPath(file string) (string, error)
}

// osPathLooker resolves binaries against the real host PATH.
type osPathLooker struct{}

func (osPathLooker) LookPath(file string) (string, error) { return exec.LookPath(file) }

// tmuxState is the immutable result of the launch-time tmux preflight.
// Both signals are captured independently so the UI can distinguish
// "tmux missing" from "tmux present but not in a session".
type tmuxState struct {
	installed   bool   // tmux is on $PATH
	inSession   bool   // process is inside a tmux session ($TMUX set)
	installCmd  string // platform-appropriate install command, "" when installed
	relaunchCmd string // copy-pasteable relaunch command
	goos        string // captured GOOS, retained for hint text
}

// relaunchCommand is the copy-pasteable command that starts the TUI inside a
// fresh tmux session. Static across platforms (tmux CLI is uniform).
const relaunchCommand = `tmux new -s human "human tui"`

// detectTmux runs the two independent preflight checks. looker, the tmux-env
// value and goos are injected so the whole result is reproducible in tests
// without real syscalls or a real host.
func detectTmux(looker pathLooker, tmuxEnv, goos string) tmuxState {
	_, err := looker.LookPath("tmux")
	installed := err == nil
	st := tmuxState{
		installed:   installed,
		inSession:   tmuxEnv != "",
		relaunchCmd: relaunchCommand,
		goos:        goos,
	}
	if !installed {
		st.installCmd = tmuxInstallCommand(goos)
	}
	return st
}

// tmuxInstallCommand returns the platform-appropriate install command,
// switching on GOOS exactly like sound.go's notificationCommand.
func tmuxInstallCommand(goos string) string {
	switch goos {
	case "darwin":
		return "brew install tmux"
	case "linux":
		if platform.IsWSL() {
			return "sudo apt-get install tmux"
		}
		return "sudo apt-get install tmux   # or: sudo dnf install tmux / sudo pacman -S tmux"
	default:
		return "see https://github.com/tmux/tmux/wiki/Installing"
	}
}

// ok reports whether agent spawn/dispatch will work: tmux installed AND in a session.
func (s tmuxState) ok() bool { return s.installed && s.inSession }

// guidance returns the actionable one-line remedy for the current state,
// used both by the banner and by the on-action status guards so the user
// sees one consistent instruction. Empty when ok().
func (s tmuxState) guidance() string {
	switch {
	case s.ok():
		return ""
	case !s.installed:
		return "tmux not installed — run: " + s.installCmd
	default: // installed but not in a session
		return "not in a tmux session — relaunch: " + s.relaunchCmd
	}
}
