//go:build wailsapp

package main

import (
	"fmt"
	"os"
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
	// Status is "ready" (a daemon was already reachable — either because it
	// already matched the working directory's project, or because a
	// different project's daemon is running and Conflict names the
	// mismatch), "auto" (no daemon was running, but a project was found —
	// either the working directory's own config or the most-recently-opened
	// project's still-existing directory — so its daemon was started
	// automatically), or "overview" (show the Projects Overview screen).
	Status  string `json:"status"`
	Project string `json:"project,omitempty"`
	Error   string `json:"error,omitempty"`
	// Orphan is true when the reachable daemon's own PID matches a
	// still-on-disk session marker whose recording app process is no longer
	// alive — left running by a crash, force-quit, or OS shutdown that never
	// reached the close flow (SC-3015). OrphanProject names the project for
	// the cleanup prompt's copy.
	Orphan        bool   `json:"orphan,omitempty"`
	OrphanProject string `json:"orphanProject,omitempty"`
	// Conflict is true when the working directory holds a valid project
	// config for a DIFFERENT project than the one a reachable daemon is
	// already serving (SC-3346). The running daemon is left exactly as it
	// was — no switch, no restart — and ConflictProject names the working
	// directory's project so the launch-time notice can name both. Choosing
	// between them interactively is a follow-up ticket; this only signals.
	Conflict        bool   `json:"conflict,omitempty"`
	ConflictProject string `json:"conflictProject,omitempty"`
}

// ProjectBootstrap resolves the launch-time screen. It is the only method
// that may start a daemon without an explicit user click (the "auto-load
// the last project" / cwd-auto-open acceptance criteria) and must be called
// once, before the frontend's first Cards() fetch.
//
// Precedence (SC-3346): the working directory the app was launched from is
// consulted FIRST — the terminal-power-user signal the rest of this method
// used to ignore entirely. Only when the working directory holds no
// recognizable project config (config.HasConfigFile is false) does this fall
// through to the original reachable-daemon -> last-recent-project ->
// Projects Overview precedence (bootstrapDefault), UNCHANGED. A malformed or
// unreadable cwd config always surfaces its own distinct error and never
// falls through silently, regardless of daemon state. Only the exact working
// directory is checked — never a parent (unlike projectRoot() in
// startproject.go, which deliberately walks up for the Start Project
// wizard's different purpose — do not reuse it here).
func (a *App) ProjectBootstrap() ProjectBootstrapResult {
	if cwd, err := os.Getwd(); err == nil {
		name, hasConfig, cfgErr := detectCwdProject(cwd)
		if cfgErr != nil {
			return ProjectBootstrapResult{
				Status: "overview",
				Error:  fmt.Sprintf("project config in %s is invalid: %s", cwd, errors.CauseChain(cfgErr)),
			}
		}
		if hasConfig {
			return a.bootstrapFromCwd(cwd, name)
		}
	}
	return a.bootstrapDefault()
}

// detectCwdProject reports the project config directly in dir, if any.
// hasConfig is false (with a nil err) when dir holds no accepted config
// filename at all — the signal that ProjectBootstrap should ignore cwd
// entirely and fall through to today's behavior. A non-nil err means a
// config file IS present but failed to parse (malformed YAML) or could not
// be read (e.g. permission denied); name is meaningless in that case.
func detectCwdProject(dir string) (name string, hasConfig bool, err error) {
	if !config.HasConfigFile(dir) {
		return "", false, nil
	}
	if verr := config.Validate(dir); verr != nil {
		return "", true, verr
	}
	name = config.ReadProjectName(dir)
	if name == "" {
		name = filepath.Base(dir)
	}
	return name, true, nil
}

// bootstrapFromCwd resolves the launch screen once a valid project config was
// found directly in the working directory. cwd is absolute (os.Getwd always
// returns an absolute path); name is that project's resolved display name.
func (a *App) bootstrapFromCwd(cwd, name string) ProjectBootstrapResult {
	if info, err := daemon.ReadInfo(); err == nil && info.IsReachable() {
		if cwdProjectRegistered(info, cwd) {
			// Already the project this daemon serves: short-circuit with no
			// restart and no state write (SC-3346 AC2).
			orphaned, orphanProject := a.checkOrphan(info)
			return ProjectBootstrapResult{Status: "ready", Project: runningProjectName(info), Orphan: orphaned, OrphanProject: orphanProject}
		}
		// A DIFFERENT project's daemon is already running: never silently
		// switch and never silently ignore cwd (SC-3346 AC3) — land on the
		// running session unchanged and name the conflict. The interactive
		// choice between them is a follow-up ticket.
		return ProjectBootstrapResult{
			Status:          "ready",
			Project:         runningProjectName(info),
			Conflict:        true,
			ConflictProject: name,
		}
	}

	cliPath, err := daemon.ResolveCLIPath(exec.LookPath)
	if err != nil {
		return ProjectBootstrapResult{Status: "overview", Error: errors.CauseChain(err)}
	}
	if err := daemon.StartForProject(daemon.DefaultRunner, cliPath, cwd, daemonStartTimeout); err != nil {
		return ProjectBootstrapResult{Status: "overview", Error: errors.CauseChain(err)}
	}
	// Same bookkeeping OpenProject does for a manually chosen directory: a
	// cwd-triggered daemon is exactly as "opened" as one picked from Projects
	// Overview, so it belongs on the recent list too.
	_ = a.recents.Touch(cwd, name)
	return ProjectBootstrapResult{Status: "auto", Project: name}
}

// bootstrapDefault is the original ProjectBootstrap precedence, unchanged by
// SC-3346: reachable daemon -> most-recently-opened project -> Projects
// Overview. Reached only when the working directory itself holds no
// recognizable project config.
func (a *App) bootstrapDefault() ProjectBootstrapResult {
	if info, err := daemon.ReadInfo(); err == nil && info.IsReachable() {
		name := ""
		if len(info.Projects) > 0 {
			name = info.Projects[0].Name
		}
		orphaned, orphanProject := a.checkOrphan(info)
		return ProjectBootstrapResult{Status: "ready", Project: name, Orphan: orphaned, OrphanProject: orphanProject}
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

// cwdProjectRegistered reports whether dir is exactly the directory of one of
// the reachable daemon's registered projects — the same absolute-path
// identity daemon.NewProjectRegistry itself uses (filepath.Abs, no symlink
// resolution), so this never invents a new notion of "same project".
func cwdProjectRegistered(info daemon.DaemonInfo, dir string) bool {
	for _, p := range info.Projects {
		if p.Dir == dir {
			return true
		}
	}
	return false
}

// runningProjectName names the (first) project a reachable daemon serves,
// for display — the same info.Projects[0].Name lookup bootstrapDefault and
// checkOrphan already use.
func runningProjectName(info daemon.DaemonInfo) string {
	if len(info.Projects) > 0 {
		return info.Projects[0].Name
	}
	return ""
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
	err = daemon.StopIfRunning(daemon.DefaultRunner, cliPath)
	// Best-effort: a cleanly stopped daemon must never later be reported as
	// orphaned at the next launch (SC-3015).
	_ = a.session.Clear()
	return err
}
