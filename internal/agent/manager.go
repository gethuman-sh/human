package agent

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/dockerhost"
)

// isDockerUnreachable reports whether err is (or wraps) a Docker daemon
// connection failure. errors.As traverses the wrap chain, so SDK connection
// errors are detected even after tozd/go/errors wrapping.
func isDockerUnreachable(err error) bool {
	return client.IsErrConnectionFailed(err)
}

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
		// A failure here is most often an unreachable Docker engine. Surface an
		// actionable error naming the active context and attempted endpoint
		// instead of the opaque generic message.
		if isDockerUnreachable(err) {
			return Meta{}, dockerhost.UnreachableError(err, dockerhost.Resolve())
		}
		return Meta{}, errors.WrapWithDetails(err, "starting agent container", "name", opts.Name)
	}

	if !opts.Interactive && opts.Prompt != "" {
		if err := m.execClaudeDetached(ctx, dcMeta.ContainerID, dcMeta.RemoteUser, opts); err != nil {
			// The agent process never started; don't leave a container tracked
			// as a running agent. Best-effort teardown, then surface the error.
			timeout := 10
			_ = m.Docker.ContainerStop(ctx, dcMeta.ContainerID, &timeout)
			_ = m.Docker.ContainerRemove(ctx, dcMeta.ContainerID, devcontainer.ContainerRemoveOptions{Force: true})
			return Meta{}, errors.WrapWithDetails(err, "launching agent process", "name", opts.Name)
		}
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

// execClaudeDetached launches the agent's `claude -p <prompt>` process inside
// the container and detaches. The prompt is passed as a discrete argv element
// (no intermediate shell), so multi-word prompts and shell metacharacters can
// neither be word-split nor injected. Errors are returned so a failed launch is
// not silently reported as a running agent.
func (m *Manager) execClaudeDetached(ctx context.Context, containerID, remoteUser string, opts StartOpts) error {
	claudeArgs := m.BuildClaudeArgs(opts)
	claudeArgs = append(claudeArgs, "-p", opts.Prompt)
	cmd := append([]string{"claude"}, claudeArgs...)
	execID, err := m.Docker.ExecCreate(ctx, containerID, cmd, devcontainer.ExecOptions{
		User: remoteUser, AttachStdout: true, AttachStderr: true,
		Env: []string{"HUMAN_AGENT_NAME=" + opts.Name},
	})
	if err != nil {
		return errors.WrapWithDetails(err, "creating agent exec")
	}
	// ExecAttach starts the exec; closing the hijacked stream detaches without
	// stopping the process, which keeps running in the container.
	attach, err := m.Docker.ExecAttach(ctx, execID)
	if err != nil {
		return errors.WrapWithDetails(err, "starting agent exec")
	}
	_ = attach.Close()
	return nil
}

// agentLocks serialises lifecycle operations per agent name. Stop/Delete can be
// invoked concurrently for the same agent by independent daemon goroutines
// (cleanup sweep, zombie sweep, an explicit stop request), each through its own
// Manager instance; the shared resource is the on-disk metadata file, so the
// lock has to live at package scope rather than on Manager.
var (
	agentLocksMu sync.Mutex
	agentLocks   = map[string]*sync.Mutex{}
)

func lockAgent(name string) func() {
	agentLocksMu.Lock()
	mu, ok := agentLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		agentLocks[name] = mu
	}
	agentLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// Stop stops and removes an agent's container.
func (m *Manager) Stop(ctx context.Context, name string) error {
	defer lockAgent(name)()
	return m.stopLocked(ctx, name)
}

// stopLocked is the body of Stop; callers must already hold the per-name lock.
func (m *Manager) stopLocked(ctx context.Context, name string) error {
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
// The whole sequence holds the per-name lock so a concurrent Stop cannot
// re-create the metadata file after DeleteMeta removes it.
func (m *Manager) Delete(ctx context.Context, name string) error {
	defer lockAgent(name)()
	_ = m.stopLocked(ctx, name)
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
