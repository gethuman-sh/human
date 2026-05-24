package devcontainer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/StephanSchmidt/human/errors"
	"github.com/StephanSchmidt/human/internal/daemon"
)

// Manager orchestrates devcontainer lifecycle operations.
type Manager struct {
	Docker DockerClient
	Logger zerolog.Logger
}

// UpOptions configures the devcontainer up operation.
type UpOptions struct {
	ProjectDir    string
	Rebuild       bool
	DaemonInfo    *daemon.DaemonInfo // nil = no daemon injection
	Out           io.Writer
	ContainerName string // override container name (default: derived from project dir)
	SourceDir     string // override mount source (default: same as ProjectDir)
}

// Up creates and starts a devcontainer. If the container already exists and is
// running, it prints a message and returns. If stopped with the same config,
// it restarts it. If the config changed, it removes the old container first.
func (m *Manager) Up(ctx context.Context, opts UpOptions) (*Meta, error) {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	projectDir, err := filepath.Abs(opts.ProjectDir)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "resolving project directory")
	}

	// 1. Find and parse devcontainer.json.
	configPath, err := FindConfig(projectDir)
	if err != nil {
		return nil, err
	}
	configData, err := os.ReadFile(configPath) // #nosec G304 -- path from FindConfig
	if err != nil {
		return nil, errors.WrapWithDetails(err, "reading devcontainer.json", "path", configPath)
	}
	cfg, err := ParseConfig(configData)
	if err != nil {
		return nil, err
	}
	cfg = ResolveVariables(cfg, projectDir)
	hash := ConfigHash(configData)
	containerName := opts.ContainerName
	if containerName == "" {
		containerName = ContainerName(projectDir)
	}

	// 2. Check for existing container with this specific name.
	existing, err := m.findContainerByName(ctx, containerName)
	if err == nil {
		meta, handleErr := m.handleExisting(ctx, existing, cfg, hash, containerName, projectDir, out)
		if handleErr == nil {
			return meta, nil
		}
		m.Logger.Info().Msg("rebuilding after config change")
	}

	// 3. Run initializeCommand on the host (not in container).
	if cfg.InitializeCommand != nil {
		_, _ = fmt.Fprintln(out, "Running initializeCommand...")
		if err := runHostCommand(cfg.InitializeCommand, projectDir); err != nil {
			return nil, errors.WrapWithDetails(err, "initializeCommand failed")
		}
	}

	return m.createFresh(ctx, cfg, projectDir, containerName, hash, opts, out)
}

// createFresh builds the image, creates and starts a new container.
func (m *Manager) createFresh(ctx context.Context, cfg *DevcontainerConfig, projectDir, containerName, hash string, opts UpOptions, out io.Writer) (*Meta, error) {
	builder := &ImageBuilder{Docker: m.Docker, Logger: m.Logger}
	_, _ = fmt.Fprintln(out, "Building devcontainer image...")
	imageID, imageName, err := builder.EnsureImage(ctx, cfg, projectDir, hash, opts.Rebuild, out)
	if err != nil {
		return nil, err
	}

	workspaceDir := cfg.WorkspaceFolder
	if workspaceDir == "" {
		workspaceDir = "/workspaces/" + filepath.Base(projectDir)
	}
	remoteUser := cfg.RemoteUser
	if remoteUser == "" {
		remoteUser = "root"
	}

	sourceDir := opts.SourceDir
	if sourceDir == "" {
		sourceDir = projectDir
	}
	// Docker bind mounts require absolute paths.
	sourceDir, _ = filepath.Abs(sourceDir)
	createOpts := m.buildCreateOptions(cfg, sourceDir, projectDir, containerName, imageName, workspaceDir, hash, opts.DaemonInfo)
	ParseRunArgs(cfg.RunArgs, &createOpts, m.Logger)

	_, _ = fmt.Fprintf(out, "Creating container %s...\n", containerName)
	containerID, err := m.Docker.ContainerCreate(ctx, createOpts)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating container", "name", containerName)
	}

	if err := m.Docker.ContainerStart(ctx, containerID); err != nil {
		return nil, errors.WrapWithDetails(err, "starting container", "id", containerID)
	}

	// Features are already baked into the image by EnsureImage.
	// Run lifecycle hooks only.
	if err := RunLifecycleHooks(ctx, m.Docker, containerID, remoteUser, cfg, m.Logger, out); err != nil {
		m.Logger.Warn().Err(err).Msg("lifecycle hooks failed, container is running but may be incomplete")
	}

	now := time.Now()
	meta := Meta{
		Name: SanitizeName(filepath.Base(projectDir)), ProjectDir: projectDir,
		ContainerID: containerID, ContainerName: containerName,
		ImageID: imageID, ImageName: imageName,
		Status: StatusRunning, CreatedAt: now, StartedAt: now,
		WorkspaceDir: workspaceDir, RemoteUser: remoteUser, ConfigHash: hash,
	}
	if opts.DaemonInfo != nil {
		meta.DaemonAddr = opts.DaemonInfo.Addr
	}
	if err := WriteMeta(meta); err != nil {
		m.Logger.Warn().Err(err).Msg("failed to persist devcontainer metadata")
	}

	_, _ = fmt.Fprintln(out)
	shortID := containerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	_, _ = fmt.Fprintf(out, "Devcontainer running: %s (%s)\n", containerName, shortID) // #nosec G705 -- CLI terminal output, not web
	_, _ = fmt.Fprintf(out, "  Workspace: %s\n", workspaceDir)
	_, _ = fmt.Fprintf(out, "  Exec:      human devcontainer exec -- bash\n")

	return &meta, nil
}

// findContainerByName looks for a managed container with the given name.
func (m *Manager) findContainerByName(ctx context.Context, name string) (ContainerSummary, error) {
	containers, err := m.Docker.ContainerList(ctx, ContainerListOptions{
		All:        true,
		NameFilter: name,
		LabelFilters: map[string]string{
			LabelManaged: "true",
		},
	})
	if err != nil {
		return ContainerSummary{}, err
	}
	// Docker's name filter is a regex match, so verify exact match.
	for _, c := range containers {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return c, nil
			}
		}
	}
	return ContainerSummary{}, errors.WithDetails("no container found", "name", name)
}

// handleExisting handles the case where a container already exists for this project.
func (m *Manager) handleExisting(ctx context.Context, existing ContainerSummary, cfg *DevcontainerConfig, hash, containerName, projectDir string, out io.Writer) (*Meta, error) {
	existingHash := existing.Labels[LabelConfigHash]

	name := SanitizeName(filepath.Base(projectDir))

	if existing.State == "running" {
		_, _ = fmt.Fprintf(out, "Devcontainer already running: %s\n", containerName)
		meta, readErr := ReadMeta(name)
		if readErr != nil {
			// Metadata missing but container exists; reconstruct and persist.
			workspaceDir := cfg.WorkspaceFolder
			if workspaceDir == "" {
				workspaceDir = "/workspaces/" + filepath.Base(projectDir)
			}
			remoteUser := cfg.RemoteUser
			if remoteUser == "" {
				remoteUser = "root"
			}
			meta = Meta{
				Name: name, ContainerID: existing.ID, ContainerName: containerName,
				ImageName: existing.Image, Status: StatusRunning, ProjectDir: projectDir,
				WorkspaceDir: workspaceDir, RemoteUser: remoteUser, ConfigHash: hash,
				CreatedAt: time.Now(),
			}
			_ = WriteMeta(meta)
		}
		return &meta, nil
	}

	// Stopped container.
	if existingHash == hash {
		_, _ = fmt.Fprintf(out, "Restarting stopped container %s...\n", containerName)
		if err := m.Docker.ContainerStart(ctx, existing.ID); err != nil {
			return nil, errors.WrapWithDetails(err, "restarting container")
		}

		remoteUser := cfg.RemoteUser
		if remoteUser == "" {
			remoteUser = "root"
		}

		if cfg.PostStartCommand != nil {
			_, _ = fmt.Fprintln(out, "Running postStartCommand...")
			if err := RunHook(ctx, m.Docker, existing.ID, remoteUser, cfg.PostStartCommand, m.Logger); err != nil {
				m.Logger.Warn().Err(err).Msg("postStartCommand failed")
			}
		}

		meta, readErr := ReadMeta(name)
		if readErr != nil {
			meta = Meta{Name: name, ContainerID: existing.ID, Status: StatusRunning, ProjectDir: projectDir}
		}
		meta.Status = StatusRunning
		meta.StartedAt = time.Now()
		if writeErr := WriteMeta(meta); writeErr != nil {
			m.Logger.Warn().Err(writeErr).Msg("failed to update metadata on restart")
		}

		_, _ = fmt.Fprintf(out, "Devcontainer restarted: %s\n", containerName)
		return &meta, nil
	}

	// Config changed: remove old container so caller can rebuild.
	_, _ = fmt.Fprintf(out, "Config changed, removing old container %s...\n", containerName)
	if rmErr := m.Docker.ContainerRemove(ctx, existing.ID, ContainerRemoveOptions{Force: true}); rmErr != nil {
		return nil, errors.WrapWithDetails(rmErr, "removing old container for rebuild")
	}
	_ = DeleteMeta(name)
	return nil, errors.WithDetails("config changed, rebuilding")
}

// buildCreateOptions creates ContainerCreateOptions from the devcontainer config.
// configDir is the directory containing .devcontainer/devcontainer.json (may differ from projectDir).
func (m *Manager) buildCreateOptions(cfg *DevcontainerConfig, projectDir, configDir, containerName, imageName, workspaceDir, hash string, daemonInfo *daemon.DaemonInfo) ContainerCreateOptions {
	env := make([]string, 0)
	for k, v := range cfg.ContainerEnv {
		env = append(env, k+"="+v)
	}
	for k, v := range cfg.RemoteEnv {
		env = append(env, k+"="+v)
	}

	// Inject daemon connectivity.
	if daemonInfo != nil {
		env = append(env,
			"HUMAN_DAEMON_ADDR="+daemon.DockerHost+":"+fmt.Sprint(daemon.DefaultPort),
			"HUMAN_DAEMON_TOKEN="+daemonInfo.Token,
			"HUMAN_CHROME_ADDR="+daemon.DockerHost+":"+fmt.Sprint(daemon.DefaultChromePort),
			"HUMAN_PROXY_ADDR="+daemon.DockerHost+":"+fmt.Sprint(daemon.DefaultProxyPort),
			"BROWSER=human-browser",
		)
	}

	binds := []string{
		projectDir + ":" + workspaceDir,
	}

	// Mount CA cert if it exists.
	home, _ := os.UserHomeDir()
	targetHome := remoteHome(cfg)

	caCert := filepath.Join(home, ".human", "ca.crt")
	if _, err := os.Stat(caCert); err == nil {
		binds = append(binds, caCert+":"+targetHome+"/.human/ca.crt:ro")
	}

	// Mount project-local Claude config so auth and plugins persist
	// across container rebuilds without touching the host's ~/.claude.
	containerClaudeDir := filepath.Join(configDir, ".devcontainer", "claude")
	if mkErr := os.MkdirAll(containerClaudeDir, 0o750); mkErr == nil {
		binds = append(binds, containerClaudeDir+":"+targetHome+"/.claude")
	}

	// Persist ~/.claude.json across container rebuilds. Claude Code stores
	// auth state here; without it each new container prompts for re-auth.
	claudeJSON := filepath.Join(containerClaudeDir, ".claude.json")
	if _, statErr := os.Stat(claudeJSON); os.IsNotExist(statErr) {
		// Seed from the most recent backup if available.
		if restored := restoreClaudeJSON(containerClaudeDir, claudeJSON); !restored {
			_ = os.WriteFile(claudeJSON, []byte("{}\n"), 0o600) // #nosec G306
		}
	}
	binds = append(binds, claudeJSON+":"+targetHome+"/.claude.json")

	// Mount host human binary so the container always uses the same version.
	if humanBin, exeErr := os.Executable(); exeErr == nil {
		binds = append(binds, humanBin+":/usr/local/bin/human:ro")
	}

	// Parse config mount strings. Devcontainer.json uses the Docker --mount
	// syntax (source=X,target=Y,type=bind) which must be converted to Binds
	// format (X:Y[:opts]).
	for _, mt := range cfg.Mounts {
		s, ok := mt.(string)
		if !ok {
			continue
		}
		if bind := parseMountString(s); bind != "" {
			binds = append(binds, bind)
		}
	}

	// Deduplicate mounts by target path. Later entries (from config) win
	// over earlier programmatic ones to avoid Docker "Duplicate mount point" errors.
	binds = deduplicateBinds(binds)

	labels := ManagedLabels(projectDir, containerName, hash)

	opts := ContainerCreateOptions{
		Name:        containerName,
		Image:       imageName,
		Cmd:         []string{"sleep", "infinity"},
		Env:         env,
		Labels:      labels,
		WorkingDir:  workspaceDir,
		Binds:       binds,
		CapAdd:      cfg.CapAdd,
		SecurityOpt: cfg.SecurityOpt,
		Privileged:  cfg.Privileged,
		ExtraHosts:  []string{"host.docker.internal:host-gateway"},
	}

	return opts
}

// parseMountString converts a devcontainer.json mount string from Docker
// --mount format ("source=X,target=Y,type=bind,readonly") to Binds format
// ("X:Y:ro"). If the string already looks like Binds format (contains ":"),
// it is returned as-is.
func parseMountString(s string) string {
	// Already in Binds format (src:dst or src:dst:opts).
	if !strings.Contains(s, "source=") && strings.Contains(s, ":") {
		return s
	}

	var source, target, mountType string
	readonly := false

	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "source", "src":
			source = v
		case "target", "dst", "destination":
			target = v
		case "type":
			mountType = v
		case "readonly":
			readonly = true
		}
	}

	// Only bind mounts can be expressed as Binds. Volume and tmpfs mounts
	// would need the Docker SDK Mounts field which we don't support yet.
	if mountType != "" && mountType != "bind" {
		return ""
	}

	if source == "" || target == "" {
		return ""
	}

	bind := source + ":" + target
	if readonly {
		bind += ":ro"
	}
	return bind
}

// deduplicateBinds removes duplicate bind mounts by target path,
// keeping the last entry for each target.
func deduplicateBinds(binds []string) []string {
	seen := make(map[string]int, len(binds))
	for i, b := range binds {
		parts := strings.SplitN(b, ":", 3)
		if len(parts) >= 2 {
			seen[parts[1]] = i
		}
	}
	result := make([]string, 0, len(seen))
	for i, b := range binds {
		parts := strings.SplitN(b, ":", 3)
		if len(parts) >= 2 && seen[parts[1]] == i {
			result = append(result, b)
		}
	}
	return result
}

// remoteHome returns the home directory path for the devcontainer's remote user.
func remoteHome(cfg *DevcontainerConfig) string {
	user := cfg.RemoteUser
	if user == "" || user == "root" {
		return "/root"
	}
	return "/home/" + user
}

// restoreClaudeJSON copies the most recent backup to claudeJSON.
// Returns true if a backup was restored.
func restoreClaudeJSON(claudeDir, claudeJSON string) bool {
	backupDir := filepath.Join(claudeDir, "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return false
	}
	// Find the most recent backup by name (timestamp suffix sorts lexically).
	var latest string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".claude.json.backup.") {
			latest = e.Name()
		}
	}
	if latest == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(backupDir, latest)) // #nosec G304 -- path from known directory
	if err != nil {
		return false
	}
	return os.WriteFile(claudeJSON, data, 0o600) == nil // #nosec G306
}

// Exec runs a command inside a running devcontainer.
func (m *Manager) Exec(ctx context.Context, containerID string, cmd []string, user string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	tty := false
	attachStdin := stdin != nil

	execID, err := m.Docker.ExecCreate(ctx, containerID, cmd, ExecOptions{
		User:         user,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  attachStdin,
		Tty:          tty,
	})
	if err != nil {
		return 1, errors.WrapWithDetails(err, "creating exec", "cmd", strings.Join(cmd, " "))
	}

	attach, err := m.Docker.ExecAttach(ctx, execID)
	if err != nil {
		return 1, errors.WrapWithDetails(err, "attaching to exec")
	}
	defer func() { _ = attach.Close() }()

	_, _ = StdCopy(stdout, stderr, attach.Reader)

	inspect, err := m.Docker.ExecInspect(ctx, execID)
	if err != nil {
		return 1, errors.WrapWithDetails(err, "inspecting exec result")
	}
	return inspect.ExitCode, nil
}

// Stop stops a running devcontainer.
func (m *Manager) Stop(ctx context.Context, name string) error {
	meta, err := ReadMeta(name)
	if err != nil {
		return errors.WrapWithDetails(err, "reading devcontainer metadata", "name", name)
	}

	timeout := 10
	if err := m.Docker.ContainerStop(ctx, meta.ContainerID, &timeout); err != nil {
		return errors.WrapWithDetails(err, "stopping container", "id", meta.ContainerID)
	}

	meta.Status = StatusStopped
	meta.StoppedAt = time.Now()
	return WriteMeta(meta)
}

// Down stops and removes a devcontainer.
func (m *Manager) Down(ctx context.Context, name string, removeVolumes bool) error {
	meta, err := ReadMeta(name)
	if err != nil {
		return errors.WrapWithDetails(err, "reading devcontainer metadata", "name", name)
	}

	if err := m.Docker.ContainerRemove(ctx, meta.ContainerID, ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: removeVolumes,
	}); err != nil {
		return errors.WrapWithDetails(err, "removing container", "id", meta.ContainerID)
	}

	return DeleteMeta(name)
}

// Status returns the current status of a devcontainer by inspecting Docker.
func (m *Manager) Status(ctx context.Context, name string) (*Meta, error) {
	meta, err := ReadMeta(name)
	if err != nil {
		return nil, err
	}

	inspect, err := m.Docker.ContainerInspect(ctx, meta.ContainerID)
	if err != nil {
		meta.Status = StatusFailed
		return &meta, nil
	}

	switch {
	case inspect.State.Running:
		meta.Status = StatusRunning
	default:
		meta.Status = StatusStopped
	}

	return &meta, nil
}

// List returns metadata for all managed devcontainers, refreshing status from Docker.
func (m *Manager) List(ctx context.Context) ([]Meta, error) {
	metas, err := ListMetas()
	if err != nil {
		return nil, err
	}

	for i := range metas {
		inspect, inspErr := m.Docker.ContainerInspect(ctx, metas[i].ContainerID)
		if inspErr != nil {
			metas[i].Status = StatusFailed
			continue
		}
		if inspect.State.Running {
			metas[i].Status = StatusRunning
		} else {
			metas[i].Status = StatusStopped
		}
	}

	return metas, nil
}

// runHostCommand executes a devcontainer.json initializeCommand on the host.
// Supports string (shell) and []interface{} (direct exec) forms.
func runHostCommand(cmd interface{}, projectDir string) error {
	switch v := cmd.(type) {
	case string:
		if v == "" {
			return nil
		}
		c := exec.Command("/bin/sh", "-c", v) // #nosec G204 -- user-controlled devcontainer.json
		c.Dir = projectDir
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	case []interface{}:
		if len(v) == 0 {
			return nil
		}
		args := make([]string, len(v))
		for i, a := range v {
			args[i] = fmt.Sprint(a)
		}
		c := exec.Command(args[0], args[1:]...) // #nosec G204 -- user-controlled devcontainer.json
		c.Dir = projectDir
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	default:
		return nil
	}
}
