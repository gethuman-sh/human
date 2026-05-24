package agent

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/StephanSchmidt/human/errors"
	"github.com/StephanSchmidt/human/internal/daemon"
	"github.com/StephanSchmidt/human/internal/devcontainer"
)

// validNameRe matches agent names: alphanumeric, hyphens, underscores.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func isValidName(name string) bool {
	return validNameRe.MatchString(name)
}

// StartOpts configures an agent start operation.
type StartOpts struct {
	Name        string
	Prompt      string
	Model       string
	SkipPerms   bool
	ConfigDir   string // where .devcontainer/devcontainer.json lives (default: cwd)
	Workspace   string // directory to mount into container (default: cwd)
	Rebuild     bool
	Interactive bool // foreground TTY mode
}

// Manager orchestrates agent lifecycle using devcontainers.
type Manager struct {
	Docker devcontainer.DockerClient
}

// Start creates a new container-based agent.
func (m *Manager) Start(ctx context.Context, opts StartOpts) (Meta, error) {
	if !isValidName(opts.Name) {
		return Meta{}, errors.WithDetails("invalid agent name: must be alphanumeric with hyphens/underscores", "name", opts.Name)
	}

	// Check for existing running agent.
	existing, err := ReadMeta(opts.Name)
	if err == nil && existing.Status == StatusRunning {
		if m.isContainerAlive(ctx, existing.ContainerID) {
			if opts.Interactive {
				// Interactive mode: reuse the running container.
				return existing, nil
			}
			return Meta{}, errors.WithDetails("agent already running", "name", opts.Name)
		}
		existing.Status = StatusStopped
		existing.StoppedAt = time.Now()
		_ = WriteMeta(existing)
	}

	containerName := ContainerPrefix + opts.Name
	workspace, configDir := resolveDirectories(opts)

	dcMeta, err := m.startDevcontainer(ctx, containerName, configDir, workspace, opts.Rebuild)
	if err != nil {
		return Meta{}, errors.WrapWithDetails(err, "starting agent container", "name", opts.Name)
	}

	if !opts.Interactive && opts.Prompt != "" {
		m.execClaudeDetached(ctx, dcMeta.ContainerID, dcMeta.RemoteUser, opts)
	}

	meta := Meta{
		Name: opts.Name, ContainerID: dcMeta.ContainerID, ContainerName: containerName,
		Cwd: workspace, Prompt: opts.Prompt,
		Status: StatusRunning, CreatedAt: time.Now(), SkipPerms: opts.SkipPerms,
		Model: opts.Model, ConfigDir: configDir, ImageName: dcMeta.ImageName,
		RemoteUser: dcMeta.RemoteUser,
	}
	if err := WriteMeta(meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func resolveDirectories(opts StartOpts) (workspace, configDir string) {
	workspace = opts.Workspace
	if workspace == "" {
		workspace = "."
	}
	configDir = opts.ConfigDir
	if configDir == "" {
		// Check .humanconfig for devcontainer.configdir.
		if hcfg, err := devcontainer.LoadHumanConfig(workspace); err == nil && hcfg.ConfigDir != "" {
			configDir = hcfg.ConfigDir
			if !filepath.IsAbs(configDir) {
				abs, _ := filepath.Abs(workspace)
				configDir = filepath.Join(abs, configDir)
			}
		} else {
			configDir = workspace
		}
	}
	return
}

func (m *Manager) startDevcontainer(ctx context.Context, containerName, configDir, workspace string, rebuild bool) (*devcontainer.Meta, error) {
	// Ensure daemon is running and reachable from containers (0.0.0.0).
	daemonInfo := m.ensureDaemonForContainers(configDir)

	dcMgr := &devcontainer.Manager{Docker: m.Docker}
	return dcMgr.Up(ctx, devcontainer.UpOptions{
		ProjectDir:    configDir,
		ContainerName: containerName,
		SourceDir:     workspace,
		Rebuild:       rebuild,
		DaemonInfo:    daemonInfo,
		Out:           os.Stderr,
	})
}

func (m *Manager) execClaudeDetached(ctx context.Context, containerID, remoteUser string, opts StartOpts) {
	claudeArgs := m.BuildClaudeArgs(opts)
	claudeArgs = append(claudeArgs, "-p", opts.Prompt)
	cmd := []string{"/bin/sh", "-c", "claude " + strings.Join(claudeArgs, " ")}
	execID, execErr := m.Docker.ExecCreate(ctx, containerID, cmd, devcontainer.ExecOptions{
		User: remoteUser, AttachStdout: true, AttachStderr: true,
		Env: []string{"HUMAN_AGENT_NAME=" + opts.Name},
	})
	if execErr == nil {
		if attach, attachErr := m.Docker.ExecAttach(ctx, execID); attachErr == nil {
			_ = attach.Close()
		}
	}
}

// Stop stops and removes an agent's container.
func (m *Manager) Stop(ctx context.Context, name string) error {
	meta, err := ReadMeta(name)
	if err != nil {
		return err
	}

	if meta.ContainerID != "" {
		timeout := 10
		_ = m.Docker.ContainerStop(ctx, meta.ContainerID, &timeout)
		_ = m.Docker.ContainerRemove(ctx, meta.ContainerID, devcontainer.ContainerRemoveOptions{Force: true})
		// Clean up devcontainer metadata to avoid stale entries.
		_ = devcontainer.DeleteMeta(meta.Name)
	}

	meta.Status = StatusStopped
	meta.StoppedAt = time.Now()
	return WriteMeta(meta)
}

// Delete stops the container and deletes the agent metadata so no trace
// remains. Best-effort: always deletes metadata even if container cleanup fails.
func (m *Manager) Delete(ctx context.Context, name string) error {
	_ = m.Stop(ctx, name)
	return DeleteMeta(name)
}

// Attach returns the container name for docker exec -it.
func (m *Manager) Attach(_ context.Context, name string) (Meta, error) {
	meta, err := ReadMeta(name)
	if err != nil {
		return Meta{}, err
	}
	if meta.ContainerName == "" {
		return Meta{}, errors.WithDetails("agent has no container", "name", name)
	}
	return meta, nil
}

// Refresh syncs metadata with actual container state.
func (m *Manager) Refresh(ctx context.Context) error {
	metas, err := ListMetas()
	if err != nil {
		return err
	}
	for _, meta := range metas {
		if meta.Status != StatusRunning {
			continue
		}
		if !m.isContainerAlive(ctx, meta.ContainerID) {
			meta.Status = StatusStopped
			meta.StoppedAt = time.Now()
			_ = WriteMeta(meta)
		}
	}
	return nil
}

// isContainerAlive checks if a container is running via Docker inspect.
func (m *Manager) isContainerAlive(ctx context.Context, containerID string) bool {
	if containerID == "" {
		return false
	}
	resp, err := m.Docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return false
	}
	return resp.State.Running
}

// BuildClaudeArgs constructs Claude Code CLI arguments.
func (m *Manager) BuildClaudeArgs(opts StartOpts) []string {
	var args []string
	if opts.SkipPerms {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args, "--permission-mode=auto")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	return args
}

// ensureDaemonForContainers makes sure the daemon is running and accessible
// from Docker containers (listening on 0.0.0.0, not just 127.0.0.1).
func (m *Manager) ensureDaemonForContainers(projectDir string) *daemon.DaemonInfo {
	info, err := daemon.ReadInfo()
	if err == nil && info.IsReachable() {
		// Daemon is running. Check if it's on localhost only.
		host, _, _ := strings.Cut(info.Addr, ":")
		if host == "127.0.0.1" || host == "localhost" {
			// Restart on 0.0.0.0 so containers can reach it.
			_, _ = fmt.Fprintln(os.Stderr, "Restarting daemon on 0.0.0.0 for container access...")
			m.restartDaemon(projectDir, "0.0.0.0")
			if newInfo, readErr := daemon.ReadInfo(); readErr == nil {
				return &newInfo
			}
		}
		return &info
	}

	// Daemon not running. Start it on 0.0.0.0.
	_, _ = fmt.Fprintln(os.Stderr, "Starting daemon for container access...")
	m.restartDaemon(projectDir, "0.0.0.0")
	if newInfo, readErr := daemon.ReadInfo(); readErr == nil {
		return &newInfo
	}
	return nil
}

// restartDaemon stops any running daemon and starts a new one on the given host.
func (m *Manager) restartDaemon(projectDir, host string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}

	_ = osexec.Command(exe, "daemon", "stop").Run() // #nosec G204 -- own binary

	addr := fmt.Sprintf("%s:%d", host, daemon.DefaultPort)
	chromeAddr := fmt.Sprintf("%s:%d", host, daemon.DefaultChromePort)
	proxyAddr := fmt.Sprintf("%s:%d", host, daemon.DefaultProxyPort)

	child := osexec.Command(exe, "daemon", "start", // #nosec G204 -- own binary
		"--addr", addr,
		"--chrome-addr", chromeAddr,
		"--proxy-addr", proxyAddr,
		"--project", projectDir,
	)
	child.Stdout = os.Stderr
	child.Stderr = os.Stderr
	_ = child.Run()

	// Poll for readiness.
	for i := 0; i < 30; i++ {
		if info, readErr := daemon.ReadInfo(); readErr == nil && info.IsReachable() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
