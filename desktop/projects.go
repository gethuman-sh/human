//go:build wailsapp

package main

import (
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/config"
	"github.com/gethuman-sh/human/internal/daemon"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// daemonStartTimeout bounds how long ProjectBootstrap/OpenProject wait for a
// freshly started daemon to become reachable before reporting failure.
const daemonStartTimeout = 5 * time.Second

// RecentProject is the frontend-facing shape of one Projects Overview entry.
type RecentProject struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// RecentProjects returns up to the 10 most recently opened projects,
// most-recent-first, for the Projects Overview screen.
func (a *App) RecentProjects() []RecentProject {
	entries := a.recents.List()
	out := make([]RecentProject, 0, len(entries))
	for _, e := range entries {
		out = append(out, RecentProject{Name: e.Name, Dir: e.Dir})
	}
	return out
}

// ProjectBootstrapResult tells the frontend which screen to render on launch.
type ProjectBootstrapResult struct {
	// Status is "ready" (a daemon was already reachable), "auto" (no daemon
	// was running, but the most-recently-opened project's directory still
	// exists, so its daemon was started automatically), or "overview" (show
	// the Projects Overview screen).
	Status  string `json:"status"`
	Project string `json:"project,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ProjectBootstrap resolves the launch-time screen. It is the only method
// that may start a daemon without an explicit user click (the "auto-load
// the last project" acceptance criterion) and must be called once, before
// the frontend's first Cards() fetch.
func (a *App) ProjectBootstrap() ProjectBootstrapResult {
	if info, err := daemon.ReadInfo(); err == nil && info.IsReachable() {
		name := ""
		if len(info.Projects) > 0 {
			name = info.Projects[0].Name
		}
		return ProjectBootstrapResult{Status: "ready", Project: name}
	}

	entries := a.recents.List()
	if len(entries) == 0 {
		return ProjectBootstrapResult{Status: "overview"}
	}
	last := entries[0]

	cliPath, err := daemon.ResolveCLIPath(exec.LookPath)
	if err != nil {
		return ProjectBootstrapResult{Status: "overview", Error: errors.CauseChain(err)}
	}
	if err := daemon.StartForProject(daemon.DefaultRunner, cliPath, last.Dir, daemonStartTimeout); err != nil {
		return ProjectBootstrapResult{Status: "overview", Error: errors.CauseChain(err)}
	}
	return ProjectBootstrapResult{Status: "auto", Project: last.Name}
}

// BrowseForProjectDir opens the native OS directory picker. Returns "" (no
// error) if the user cancels.
func (a *App) BrowseForProjectDir() (string, error) {
	dir, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Open a human project",
	})
	if err != nil {
		return "", errors.WrapWithDetails(err, "opening directory picker")
	}
	return dir, nil
}

// OpenProject stops any daemon currently running, starts one scoped to dir,
// records dir as the most-recently-opened project, and returns once the new
// daemon is reachable.
func (a *App) OpenProject(dir string) (RecentProject, error) {
	if !config.HasConfigFile(dir) {
		return RecentProject{}, errors.WithDetails("directory has no .humanconfig.yaml", "dir", dir)
	}
	cliPath, err := daemon.ResolveCLIPath(exec.LookPath)
	if err != nil {
		return RecentProject{}, err
	}
	if err := daemon.StopIfRunning(daemon.DefaultRunner, cliPath); err != nil {
		return RecentProject{}, err
	}
	if err := daemon.StartForProject(daemon.DefaultRunner, cliPath, dir, daemonStartTimeout); err != nil {
		return RecentProject{}, err
	}
	name := config.ReadProjectName(dir)
	if name == "" {
		name = filepath.Base(dir)
	}
	// Losing the recent-list bump is a display nuisance, not a reason to
	// fail an otherwise-successful project open.
	_ = a.recents.Touch(dir, name)
	return RecentProject{Name: name, Dir: dir}, nil
}

// SwitchProject stops the running daemon so the frontend can show the
// Projects Overview screen. The recent-projects list is untouched — the
// project stays "recent" even though its daemon is now stopped.
func (a *App) SwitchProject() error {
	cliPath, err := daemon.ResolveCLIPath(exec.LookPath)
	if err != nil {
		return err
	}
	return daemon.StopIfRunning(daemon.DefaultRunner, cliPath)
}
