//go:build wailsapp

package main

import (
	"os/exec"

	"github.com/gethuman-sh/human/internal/appsession"
	"github.com/gethuman-sh/human/internal/daemon"
)

// checkOrphan evaluates the on-disk session marker against the given
// reachable daemon's own PID, using the real process-liveness check. Split
// out from ProjectBootstrap so the pure decision (appsession.IsOrphaned) is
// exercised under test without a real daemon or process table.
func (a *App) checkOrphan(info daemon.DaemonInfo) (orphaned bool, projectName string) {
	marker, present := a.session.Read()
	if !appsession.IsOrphaned(marker, present, info.PID, daemon.IsProcessAlive) {
		return false, ""
	}
	if len(info.Projects) > 0 {
		return true, info.Projects[0].Name
	}
	return true, ""
}

// ResolveOrphan answers the launch-time "leftover daemon" prompt (see
// ProjectBootstrapResult.Orphan). stop=true stops the daemon the same way
// SwitchProject does; stop=false leaves it running and clears the session
// marker, which reclassifies it as a user-intentional standalone daemon so no
// future launch nags about it again unless a fresh app session re-marks it
// (SC-3015).
func (a *App) ResolveOrphan(stop bool) error {
	_ = a.session.Clear()
	if !stop {
		return nil
	}
	cliPath, err := daemon.ResolveCLIPath(exec.LookPath)
	if err != nil {
		return err
	}
	return daemon.StopIfRunning(daemon.DefaultRunner, cliPath)
}
