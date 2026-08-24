package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
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
	// GitDir is the repository's common .git directory to bind at its
	// host-identical path when SourceDir is a git worktree: a worktree's .git
	// FILE references the object store by absolute host path, so without this
	// bind every in-container git command fails with "not a git repository"
	// (ticket 482). The COMMON dir, because a worktree of a worktree resolves
	// through the same one and a per-worktree .git is only a pointer to it
	// (SC-4595). Empty for shared-checkout and non-git workspaces, whose .git
	// travels with the source mount itself.
	GitDir string

	// cacheVolumes is populated by Up from the project's .humanconfig caches
	// section — callers never set it directly.
	cacheVolumes []CacheVolume
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

	// Project-declared cache volumes ride every container of this project so
	// consecutive runs build warm (SC-783). Loaded here so both the agent and
	// direct devcontainer paths get them.
	opts.cacheVolumes = m.loadCacheVolumes(projectDir, out)

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
	img, err := builder.EnsureImage(ctx, cfg, projectDir, hash, opts.Rebuild, out)
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
	createOpts := m.buildCreateOptions(cfg, sourceDir, projectDir, containerName, img.Name, workspaceDir, hash, opts.DaemonInfo, opts.GitDir, opts.cacheVolumes)
	ParseRunArgs(cfg.RunArgs, &createOpts, m.Logger)

	_, _ = fmt.Fprintf(out, "Creating container %s...\n", containerName)
	containerID, err := m.Docker.ContainerCreate(ctx, createOpts)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating container", "name", containerName)
	}

	if err := m.Docker.ContainerStart(ctx, containerID); err != nil {
		return nil, errors.WrapWithDetails(err, "starting container", "id", containerID)
	}

	if err := m.prepareCacheVolumes(ctx, containerID, remoteUser, opts.cacheVolumes); err != nil {
		// A cache that cannot be made writable must only cost the warm start,
		// never the run — the build falls back to an in-container cold cache.
		_, _ = fmt.Fprintf(out, "Warning: cache volumes not writable, continuing cold: %v\n", err)
	}

	// Features are already baked into the image by EnsureImage.
	// Run lifecycle hooks only.
	if err := RunLifecycleHooks(ctx, m.Docker, containerID, remoteUser, cfg, m.Logger, out); err != nil {
		reportHookFailure(m.Logger, out, "lifecycle hooks", err)
	}

	now := time.Now()
	meta := Meta{
		Name: SanitizeName(filepath.Base(projectDir)), ProjectDir: projectDir,
		ContainerID: containerID, ContainerName: containerName,
		ImageID: img.ID, ImageName: img.Name,
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

	// Only reuse a running container when its build config is unchanged.
	// Otherwise fall through to the removal/rebuild path below so an edited
	// devcontainer.json actually takes effect instead of silently reusing the
	// stale container.
	if existing.State == "running" && existingHash == hash {
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
				reportHookFailure(m.Logger, out, "postStartCommand", err)
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
func (m *Manager) buildCreateOptions(cfg *DevcontainerConfig, projectDir, configDir, containerName, imageName, workspaceDir, hash string, daemonInfo *daemon.DaemonInfo, gitDir string, caches []CacheVolume) ContainerCreateOptions {
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
			"HUMAN_PROXY_ADDR="+proxyAddrForContainer(ContainerReachableHost(), daemon.DefaultProxyPort),
			"BROWSER=human-browser",
		)
	}

	binds := []Mount{Bind(projectDir, workspaceDir)}

	// Project-declared persistent caches: a bind whose source is not a path is
	// a named volume, which Docker auto-creates on first use — no extra API.
	for _, c := range caches {
		binds = append(binds, Bind(c.VolumeName(), c.Path))
	}

	// A worktree workspace resolves git through the parent repo's .git, which
	// lives outside the mount — bind it at its host-identical path so the
	// worktree's absolute gitdir pointer works in-container (ticket 482).
	if gitDir != "" {
		binds = append(binds, Bind(gitDir, gitDir))
	}

	// Mount CA cert if it exists.
	home, _ := os.UserHomeDir()
	targetHome := remoteHome(cfg)

	caCert := filepath.Join(home, ".human", "ca.crt")
	if IsValidCACertFile(caCert) {
		binds = append(binds, Bind(caCert, targetHome+"/.human/ca.crt").ReadOnly())
	}

	// Mount project-local Claude config so auth and plugins persist
	// across container rebuilds without touching the host's ~/.claude.
	containerClaudeDir := filepath.Join(configDir, ".devcontainer", "claude")
	if mkErr := os.MkdirAll(containerClaudeDir, 0o750); mkErr == nil {
		binds = append(binds, Bind(containerClaudeDir, targetHome+"/.claude"))
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
	binds = append(binds, Bind(claudeJSON, targetHome+"/.claude.json"))

	// Mount host human binary so the container always uses the same version —
	// but only one the container can actually execute. The image ships a linux
	// human at this path already, so binding a macOS build over it does not pin
	// a version, it replaces a working binary with one that answers "Exec format
	// error" — and takes the lifecycle hooks with it, since postStartCommand
	// installs the proxy CA by running human (SC-4596).
	if humanBin, exeErr := os.Executable(); exeErr == nil && runsInContainer(humanBin) {
		binds = append(binds, Bind(humanBin, "/usr/local/bin/human").ReadOnly())
	}

	// Parse config mount strings. Devcontainer.json uses the Docker --mount
	// syntax (source=X,target=Y,type=bind) which must be converted to Binds
	// format (X:Y[:opts]).
	for _, mt := range cfg.Mounts {
		s, ok := mt.(string)
		if !ok {
			continue
		}
		if mt, ok := parseMountString(s); ok {
			binds = append(binds, mt)
		}
	}

	// Deduplicate mounts by target path. Later entries (from config) win
	// over earlier programmatic ones to avoid Docker "Duplicate mount point" errors.
	binds = dedupeMounts(binds)

	labels := ManagedLabels(projectDir, containerName, hash)

	opts := ContainerCreateOptions{
		Name:  containerName,
		Image: imageName,
		Cmd:   []string{"sleep", "infinity"},
		// `sleep` is PID 1 here and reaps nothing, so an agent that exits leaves
		// its children — the git and human processes it spawned — defunct until
		// the container is removed. An init reaps them as they die (SC-4281).
		Init:        true,
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

// parseMountString reads a mount as devcontainer.json writes it. The spec's
// own form is the Docker --mount syntax ("source=X,target=Y,type=bind"); a
// plain bind string is also accepted, because projects write those too. It
// reports false for anything this package cannot express as a bind — a volume
// or tmpfs mount, or a mount naming only one side.
func parseMountString(s string) (Mount, bool) {
	// A plain bind string: no --mount keys, so read it as source:target[:opts].
	if !strings.Contains(s, "source=") && strings.Contains(s, ":") {
		return ParseBind(s)
	}

	var source, target, mountType string
	readonly := false

	for part := range strings.SplitSeq(s, ",") {
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
		return Mount{}, false
	}
	if source == "" || target == "" {
		return Mount{}, false
	}

	m := Bind(source, target)
	if readonly {
		m = m.ReadOnly()
	}
	return m, true
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
func runHostCommand(cmd any, projectDir string) error {
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
	case []any:
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

// loadCacheVolumes reads the project's declared cache volumes, dropping
// invalid entries with a warning — a bad declaration costs the warm start,
// never the launch.
func (m *Manager) loadCacheVolumes(projectDir string, out io.Writer) []CacheVolume {
	all, err := LoadCaches(projectDir)
	if err != nil {
		_, _ = fmt.Fprintf(out, "Warning: ignoring caches config: %v\n", err)
		return nil
	}
	var valid []CacheVolume
	for _, c := range all {
		if c.Valid() {
			valid = append(valid, c)
			continue
		}
		_, _ = fmt.Fprintf(out, "Warning: ignoring invalid cache volume %q (need a docker-safe name and an absolute path)\n", c.Name) // #nosec G705 -- CLI terminal output, not web
	}
	return valid
}

// prepareCacheVolumes makes freshly created cache volumes writable for the
// remote user: Docker creates an empty named volume root-owned, so a non-root
// container's first run could not write its cache. Only the volume roots are
// chowned (non-recursive) — everything below is created by the remote user
// afterwards, so the top-level fix is sufficient and idempotent across runs.
func (m *Manager) prepareCacheVolumes(ctx context.Context, containerID, remoteUser string, caches []CacheVolume) error {
	if len(caches) == 0 || remoteUser == "" || remoteUser == "root" {
		return nil
	}
	paths := make([]string, 0, len(caches))
	for _, c := range caches {
		paths = append(paths, c.Path)
	}
	// Discrete argv (no shell) so paths never need quoting.
	if err := execInContainer(ctx, m.Docker, containerID, "root", append([]string{"mkdir", "-p"}, paths...), nil, m.Logger); err != nil {
		return err
	}
	return execInContainer(ctx, m.Docker, containerID, "root", append([]string{"chown", remoteUser}, paths...), nil, m.Logger)
}

// elfMagic is the first four bytes of every ELF executable — the format a linux
// container can run.
var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// runsInContainer reports whether the host binary at path is in the container's
// executable format, so a bind of it replaces the image's binary with something
// that still runs.
//
// Format, not architecture: an ELF built for another CPU than the container's
// still fails, and detecting that means reading the ELF header's machine field
// for a mapping to Docker's platform strings. The case this exists for is the
// whole-format mismatch a macOS or Windows host produces, which is one read of
// four bytes. An unreadable binary answers false — the image's own binary is a
// working fallback, and a failed launch is not.
func runsInContainer(path string) bool {
	f, err := os.Open(path) // #nosec G304 -- path is this process's own executable
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	header := make([]byte, len(elfMagic))
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return bytes.Equal(header, elfMagic)
}
