//go:build !windows

package cmddaemon

import (
	"os"
	"syscall"
)

// detachSysProcAttr returns SysProcAttr that creates a new session,
// so the child process survives terminal close.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// isProcessAlive checks whether a process with the given PID is still running.
func isProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// stopProcess sends SIGTERM to the process with the given PID.
func stopProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// killProcess ends the process with the given PID outright. It exists so
// `human daemon stop --force` has an answer for a wedged daemon that names ONE
// pid — the reach for `killall human` matches every process of that name, which
// on this machine includes the `human deploy` a user is waiting on.
func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
