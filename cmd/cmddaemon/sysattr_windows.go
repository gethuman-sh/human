//go:build windows

package cmddaemon

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachSysProcAttr returns SysProcAttr for Windows (no Setsid equivalent needed).
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// isProcessAlive checks whether a process with the given PID is still running.
// On Windows, os.FindProcess always succeeds, so we open a process handle and
// check the exit code instead.
func isProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	// STILL_ACTIVE (259) means the process has not exited yet.
	const stillActive = 259
	return exitCode == stillActive
}

// stopProcess kills the process with the given PID.
// Windows lacks SIGTERM, so we use Kill().
func stopProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// killProcess ends the process outright. Windows has no gentler stop to
// distinguish it from, so it is the same call — the flag still matters because
// it names one pid instead of every process called "human".
func killProcess(pid int) error {
	return stopProcess(pid)
}
