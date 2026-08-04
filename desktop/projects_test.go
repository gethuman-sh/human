//go:build wailsapp

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/gethuman-sh/human/internal/appsession"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/recentprojects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestApp builds an App whose local stores are isolated under a fresh
// temp directory, so tests never touch the developer's real ~/.human state.
func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	return &App{
		recents: recentprojects.NewStore(filepath.Join(dir, "recentprojects.json")),
		session: appsession.NewStore(filepath.Join(dir, "appsession.json")),
	}
}

// writeConfig writes a minimal .humanconfig.yaml into dir, optionally naming
// the project.
func writeConfig(t *testing.T, dir, project string) {
	t.Helper()
	body := "githubs: []\n"
	if project != "" {
		body = "project: " + project + "\n" + body
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(body), 0o644))
}

// listenForDaemon starts a real TCP listener and records it as a reachable
// daemon serving the given projects — the same idiom
// internal/daemon/lifecycle_test.go uses for IsReachable().
func listenForDaemon(t *testing.T, projects []daemon.ProjectInfo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	require.NoError(t, daemon.WriteInfo(daemon.DaemonInfo{Addr: ln.Addr().String(), Projects: projects}))
	t.Cleanup(daemon.RemoveInfo)
}

// noDaemonReachable points daemon.ReadInfo at an isolated, empty HOME so no
// daemon.json (and therefore no reachable daemon) is found.
func noDaemonReachable(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestDetectCwdProject_noConfig(t *testing.T) {
	name, hasConfig, err := detectCwdProject(t.TempDir())

	require.NoError(t, err)
	assert.False(t, hasConfig)
	assert.Empty(t, name)
}

func TestDetectCwdProject_validConfig_named(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "infra")

	name, hasConfig, err := detectCwdProject(dir)

	require.NoError(t, err)
	assert.True(t, hasConfig)
	assert.Equal(t, "infra", name)
}

func TestDetectCwdProject_validConfig_unnamedUsesDirBase(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "")

	name, hasConfig, err := detectCwdProject(dir)

	require.NoError(t, err)
	assert.True(t, hasConfig)
	assert.Equal(t, filepath.Base(dir), name)
}

func TestDetectCwdProject_malformed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(":\n  bad: [yaml\n"), 0o644))

	_, hasConfig, err := detectCwdProject(dir)

	require.Error(t, err)
	assert.True(t, hasConfig)
}

func TestCwdProjectRegistered(t *testing.T) {
	info := daemon.DaemonInfo{Projects: []daemon.ProjectInfo{{Name: "infra", Dir: "/projects/infra"}}}

	assert.True(t, cwdProjectRegistered(info, "/projects/infra"))
	assert.False(t, cwdProjectRegistered(info, "/projects/other"))
}

func TestRunningProjectName(t *testing.T) {
	assert.Equal(t, "", runningProjectName(daemon.DaemonInfo{}))
	assert.Equal(t, "infra", runningProjectName(daemon.DaemonInfo{Projects: []daemon.ProjectInfo{{Name: "infra", Dir: "/x"}}}))
}

func TestProjectBootstrap_cwdMatchesRunningDaemon_shortCircuits(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "infra")
	t.Chdir(dir)
	listenForDaemon(t, []daemon.ProjectInfo{{Name: "infra", Dir: dir}})

	a := newTestApp(t)
	result := a.ProjectBootstrap()

	assert.Equal(t, "ready", result.Status)
	assert.Equal(t, "infra", result.Project)
	assert.False(t, result.Conflict)
}

func TestProjectBootstrap_cwdConflictsWithRunningDaemon_signalsConflict(t *testing.T) {
	cwdDir := t.TempDir()
	writeConfig(t, cwdDir, "here")
	t.Chdir(cwdDir)
	otherDir := t.TempDir()
	listenForDaemon(t, []daemon.ProjectInfo{{Name: "there", Dir: otherDir}})

	a := newTestApp(t)
	result := a.ProjectBootstrap()

	assert.Equal(t, "ready", result.Status)
	assert.Equal(t, "there", result.Project)
	assert.True(t, result.Conflict)
	assert.Equal(t, "here", result.ConflictProject)
}

func TestProjectBootstrap_malformedCwdConfig_surfacesDistinctError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(":\n  bad: [yaml\n"), 0o644))
	t.Chdir(dir)
	noDaemonReachable(t)

	a := newTestApp(t)
	result := a.ProjectBootstrap()

	assert.Equal(t, "overview", result.Status)
	assert.Contains(t, result.Error, "is invalid")
	assert.Contains(t, result.Error, dir)
}

func TestProjectBootstrap_cwdValidConfig_noDaemon_cliNotFound(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "infra")
	t.Chdir(dir)
	noDaemonReachable(t)
	t.Setenv("PATH", "")

	a := newTestApp(t)
	result := a.ProjectBootstrap()

	assert.Equal(t, "overview", result.Status)
	assert.Contains(t, result.Error, "human CLI not found")
}

func TestProjectBootstrap_noCwdConfig_noDaemonNoRecents_fallsThroughToOverview(t *testing.T) {
	t.Chdir(t.TempDir())
	noDaemonReachable(t)

	a := newTestApp(t)
	result := a.ProjectBootstrap()

	assert.Equal(t, "overview", result.Status)
	assert.Empty(t, result.Error)
	assert.False(t, result.Conflict)
}

func TestProjectBootstrap_noCwdConfig_daemonReachable_behavesUnchanged(t *testing.T) {
	t.Chdir(t.TempDir())
	otherDir := t.TempDir()
	listenForDaemon(t, []daemon.ProjectInfo{{Name: "elsewhere", Dir: otherDir}})

	a := newTestApp(t)
	result := a.ProjectBootstrap()

	assert.Equal(t, "ready", result.Status)
	assert.Equal(t, "elsewhere", result.Project)
	assert.False(t, result.Conflict)
}
