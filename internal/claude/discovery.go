package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gethuman-sh/human/errors"

	"github.com/rs/zerolog/log"
)

// MemoryInfo holds memory usage and limit for a container.
type MemoryInfo struct {
	Usage uint64 // current memory usage in bytes
	Limit uint64 // memory limit in bytes (0 = unlimited)
}

// Instance represents a discovered Claude Code instance.
type Instance struct {
	Label           string      // e.g. "Host (PID 7046)" or `Container "dev-myapp" (abc123)`
	Source          string      // "host" or "container"
	Walker          DirWalker   // how to read its JSONL data
	Root            string      // JSONL root path (or virtual path for containers)
	Memory          *MemoryInfo // memory usage (containers only)
	ContainerID     string      // full Docker container ID (containers only)
	PID             int         // host PID of the claude process (0 for containers)
	Cwd             string      // working directory of the Claude process
	FilePath        string      // resolved JSONL path for fsnotify (host instances only)
	ProxyConfigured bool        // true if the instance is configured to use the daemon's HTTPS proxy
	DaemonConnected bool        // true if the instance has recently communicated with the daemon
}

// InstanceFinder discovers running Claude Code instances.
type InstanceFinder interface {
	FindInstances(ctx context.Context) ([]Instance, error)
}

// CommandRunner abstracts running external commands for testability.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSCommandRunner implements CommandRunner using os/exec.
type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() // #nosec G204 — only called with hardcoded commands
}

// DockerClient abstracts Docker operations for testability.
type DockerClient interface {
	ListContainers(ctx context.Context) ([]ContainerInfo, error)
	Exec(ctx context.Context, containerID string, cmd []string) (int, io.Reader, error)
	ContainerStats(ctx context.Context, containerID string) (*MemoryInfo, error)
	Close() error
}

// ContainerInfo holds minimal container metadata.
type ContainerInfo struct {
	ID     string
	Name   string
	Labels map[string]string
}

// ContainerChecker determines whether a process is running inside a container.
type ContainerChecker interface {
	IsContainerized(pid int) bool
}

// ProcContainerChecker reads /proc/<pid>/cgroup to detect containerized processes.
type ProcContainerChecker struct{}

func (ProcContainerChecker) IsContainerized(pid int) bool {
	// RC-11: If we ourselves are containerized, don't skip sibling processes.
	if isSelfContainerized() {
		return false
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") ||
		strings.Contains(s, "/lxc/") || strings.Contains(s, "/kubepods")
}

// CwdResolver resolves the current working directory for a process.
type CwdResolver interface {
	ResolveCwd(pid int) (string, error)
}

// ProcCwdResolver reads /proc/<pid>/cwd (Linux).
type ProcCwdResolver struct{}

func (ProcCwdResolver) ResolveCwd(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}

// SessionResolver resolves the session ID for a Claude process.
type SessionResolver interface {
	ResolveSessionID(pid int) (string, error)
}

// FileSessionResolver reads session info from ~/.claude/sessions/<PID>.json.
type FileSessionResolver struct {
	HomeDir string
}

func (r FileSessionResolver) ResolveSessionID(pid int) (string, error) {
	path := filepath.Join(r.HomeDir, ".claude", "sessions", fmt.Sprintf("%d.json", pid))
	data, err := os.ReadFile(path) // #nosec G304 — path constructed from trusted home dir + PID
	if err != nil {
		return "", err
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return "", err
	}
	if session.SessionID == "" {
		return "", errors.WithDetails("empty sessionId", "path", path)
	}
	return session.SessionID, nil
}

// resolveJSONLPath finds the active JSONL for a PID.
// It tries the session file first (needed when multiple Claudes share a project dir),
// then falls back to the newest JSONL by mtime only when no session file exists —
// Claude creates new conversations without updating the session file.
//
// When the session file resolves a session ID but the JSONL doesn't exist yet
// (startup race — JSONL is created seconds after the session file), we return ""
// rather than falling back to the newest JSONL, which would be the previous
// session's file and cause stale subagents/tasks to appear.
func resolveJSONLPath(sess SessionResolver, pid int, root string) string {
	sessionID, err := sess.ResolveSessionID(pid)
	if err == nil {
		sessionPath := filepath.Clean(filepath.Join(root, sessionID+".jsonl"))
		if _, fErr := os.Stat(sessionPath); fErr == nil { // #nosec G703 -- root is the Claude config dir, sessionID from local session file
			return sessionPath
		}
		// Session file exists and points to a known session ID, but the JSONL
		// hasn't been created yet. Return empty — the hook-based state from
		// fillMissingFromHooks will provide status until the JSONL appears.
		return ""
	}
	// No session file at all (e.g. old Claude version). Fall back to newest JSONL.
	if newest, err := findNewestJSONL(root); err == nil && newest != "" {
		return newest
	}
	return ""
}

// CwdToProjectDir converts an absolute cwd to the Claude project subdir name.
// e.g. "/home/user/project" -> "-home-user-project"
func CwdToProjectDir(cwd string) string {
	return strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
}

// ShortProjectName returns the last two path components for a readable label.
// e.g. "/home/user/dev/myproject" -> "dev/myproject"
func ShortProjectName(cwd string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(cwd)), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return cwd
}

// CommChecker verifies a process command name.
type CommChecker func(pid int) bool

// PPIDResolver resolves the parent PID for a process.
type PPIDResolver interface {
	ResolvePPID(pid int) int
}

// ProcPPIDResolver reads /proc/<pid>/status to get the parent PID.
type ProcPPIDResolver struct{}

func (ProcPPIDResolver) ResolvePPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)) // #nosec G304 — pid is an integer
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			ppid, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			return ppid
		}
	}
	return 0
}

// HostFinder discovers Claude Code instances on the local host via pgrep.
type HostFinder struct {
	Runner           CommandRunner
	HomeDir          string           // override for testing; empty uses os.UserHomeDir result passed externally
	CwdResolver      CwdResolver      // nil defaults to ProcCwdResolver
	ContainerChecker ContainerChecker // nil defaults to ProcContainerChecker
	SessionResolver  SessionResolver  // nil defaults to FileSessionResolver{HomeDir: h.HomeDir}
	CommChecker      CommChecker      // nil defaults to verifyProcComm
	PPIDResolver     PPIDResolver     // nil defaults to ProcPPIDResolver
}

// remoteServerInfo holds metadata for a discovered "claude remote" server process.
type remoteServerInfo struct {
	PID int
	Cwd string
}

func (h *HostFinder) FindInstances(ctx context.Context) ([]Instance, error) {
	resolver := h.CwdResolver
	if resolver == nil {
		resolver = ProcCwdResolver{}
	}

	ctrChecker := h.ContainerChecker
	if ctrChecker == nil {
		ctrChecker = ProcContainerChecker{}
	}

	sessResolver := h.SessionResolver
	if sessResolver == nil {
		sessResolver = FileSessionResolver{HomeDir: h.HomeDir}
	}

	ppidResolver := h.PPIDResolver
	if ppidResolver == nil {
		ppidResolver = ProcPPIDResolver{}
	}

	// Phase 1: discover "claude remote" server processes.
	remoteServers := h.findRemoteServers(ctx, resolver)
	remotePIDs := make(map[int]bool, len(remoteServers))
	for pid := range remoteServers {
		remotePIDs[pid] = true
	}

	// Phase 2: discover regular claude processes, grouping remote children.
	hostInstances, remoteChildren := h.findHostProcesses(ctx, resolver, ctrChecker, sessResolver, ppidResolver, remotePIDs)

	// Phase 3: build one consolidated Instance per remote server.
	var instances []Instance
	for _, info := range remoteServers {
		children := remoteChildren[info.PID]
		instances = append(instances, buildRemoteInstance(h.HomeDir, info, children))
	}
	instances = append(instances, hostInstances...)

	return instances, nil
}

// findRemoteServers discovers "claude remote" server processes via pgrep -f.
// Returns a map of server PID → server info (no Instance entries emitted yet).
func (h *HostFinder) findRemoteServers(ctx context.Context, resolver CwdResolver) map[int]remoteServerInfo {
	servers := map[int]remoteServerInfo{}

	remoteOut, remoteErr := h.Runner.Run(ctx, "pgrep", "-af", "claude remote")
	if remoteErr != nil {
		return servers
	}

	scanner := bufio.NewScanner(bytes.NewReader(remoteOut))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}
		pid := parts[0]
		cmdLine := parts[1]

		if !isClaudeRemoteCmd(cmdLine) {
			continue
		}

		pidNum, err := strconv.Atoi(pid)
		if err != nil {
			continue
		}

		cwd, err := resolver.ResolveCwd(pidNum)
		if err != nil {
			log.Debug().Err(err).Int("pid", pidNum).Msg("cannot resolve cwd for remote server, skipping")
			continue
		}

		servers[pidNum] = remoteServerInfo{PID: pidNum, Cwd: cwd}
	}
	return servers
}

// findHostProcesses discovers regular claude processes via pgrep -a.
// Children of remote servers are grouped by server PID instead of returned directly.
func (h *HostFinder) findHostProcesses(ctx context.Context, resolver CwdResolver, ctrChecker ContainerChecker, sessResolver SessionResolver, ppidResolver PPIDResolver, remotePIDs map[int]bool) ([]Instance, map[int][]Instance) {
	remoteChildren := make(map[int][]Instance)

	out, err := h.Runner.Run(ctx, "pgrep", "-a", "claude")
	if err != nil {
		return nil, remoteChildren
	}

	commCheck := h.CommChecker
	if commCheck == nil {
		commCheck = verifyProcComm
	}

	var instances []Instance
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}
		pid := parts[0]
		cmdLine := parts[1]

		cmdParts := strings.Fields(cmdLine)
		if len(cmdParts) == 0 {
			continue
		}
		if filepath.Base(cmdParts[0]) != "claude" {
			continue
		}

		pidNum, err := strconv.Atoi(pid)
		if err != nil {
			continue
		}

		if remotePIDs[pidNum] {
			continue
		}

		if !commCheck(pidNum) {
			log.Debug().Int("pid", pidNum).Msg("proc comm mismatch, skipping")
			continue
		}

		if ctrChecker.IsContainerized(pidNum) {
			log.Trace().Int("pid", pidNum).Msg("skipping containerized process")
			continue
		}

		cwd, err := resolver.ResolveCwd(pidNum)
		if err != nil {
			log.Debug().Err(err).Int("pid", pidNum).Msg("cannot resolve cwd, skipping")
			continue
		}

		projectDir := CwdToProjectDir(cwd)
		root := filepath.Join(h.HomeDir, ".claude", "projects", projectDir)
		filePath := resolveJSONLPath(sessResolver, pidNum, root)

		inst := Instance{
			Source:   "host",
			Walker:   OSDirWalker{},
			Root:     root,
			PID:      pidNum,
			Cwd:      cwd,
			FilePath: filePath,
		}

		if ppid := ppidResolver.ResolvePPID(pidNum); remotePIDs[ppid] {
			remoteChildren[ppid] = append(remoteChildren[ppid], inst)
		} else {
			inst.Label = fmt.Sprintf("Host: %s (PID %s)", ShortProjectName(cwd), pid)
			instances = append(instances, inst)
		}
	}
	return instances, remoteChildren
}

// buildRemoteInstance creates a single consolidated Instance for a remote server
// and its child sessions. The label lists all PIDs (server + children).
// If children exist, the first child's JSONL data is used for session/usage display.
func buildRemoteInstance(homeDir string, server remoteServerInfo, children []Instance) Instance {
	pids := []string{strconv.Itoa(server.PID)}
	for _, c := range children {
		pids = append(pids, strconv.Itoa(c.PID))
	}

	label := fmt.Sprintf("Host (R): %s (PID %s)", ShortProjectName(server.Cwd), strings.Join(pids, ", "))

	projectDir := CwdToProjectDir(server.Cwd)
	root := filepath.Join(homeDir, ".claude", "projects", projectDir)

	inst := Instance{
		Label:  label,
		Source: "remote",
		Walker: OSDirWalker{},
		Root:   root,
		PID:    server.PID,
		Cwd:    server.Cwd,
	}

	if len(children) > 0 {
		inst.Walker = children[0].Walker
		inst.Root = children[0].Root
		inst.FilePath = children[0].FilePath
	}

	return inst
}

// isClaudeRemoteCmd checks if a command line represents a "claude remote" server invocation.
// It matches server-mode subcommands ("claude remote", "claude remote-control") but NOT
// interactive-mode flags ("claude --remote-control", "claude --rc") since those sessions
// are locally controllable and should not be labeled (R) in the TUI.
// It also filters false positives like grep/zsh eval matches by requiring the claude
// token to be a path (contains /) or the first token in the command.
func isClaudeRemoteCmd(cmdLine string) bool {
	fields := strings.Fields(cmdLine)
	for i, f := range fields {
		if filepath.Base(f) != "claude" {
			continue
		}
		if i+1 >= len(fields) {
			continue
		}
		next := fields[i+1]
		if next != "remote" && next != "remote-control" {
			continue
		}
		// Accept if the claude token is a path (contains /) or is the first token
		// in the command (bare "claude remote").
		if strings.Contains(f, "/") || i == 0 {
			return true
		}
	}
	return false
}

// verifyProcComm checks that /proc/<pid>/comm matches "claude".
func verifyProcComm(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)) // #nosec G703 -- pid is an integer from os process listing
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "claude"
}

// isSelfContainerized checks if the current process is running inside a container.
func isSelfContainerized() bool {
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") ||
		strings.Contains(s, "/lxc/") || strings.Contains(s, "/kubepods")
}

// dockerCacheEntry holds cached container JSONL data with a TTL.
type dockerCacheEntry struct {
	data      []byte
	fetchedAt time.Time
}

// DockerFinder discovers Claude Code instances inside Docker containers.
type DockerFinder struct {
	Client   DockerClient
	CacheTTL time.Duration // TTL for container data cache; defaults to 2s

	mu    sync.Mutex
	cache map[string]*dockerCacheEntry
}

func (d *DockerFinder) FindInstances(ctx context.Context) ([]Instance, error) {
	containers, err := d.Client.ListContainers(ctx)
	if err != nil {
		return nil, err
	}

	ttl := d.CacheTTL
	if ttl == 0 {
		ttl = 2 * time.Second
	}

	type result struct {
		inst Instance
		ok   bool
	}
	ch := make(chan result, len(containers))
	for _, ctr := range containers {
		go func(ctr ContainerInfo) {
			inst, ok := d.buildContainerInstance(ctx, ctr, ttl)
			ch <- result{inst, ok}
		}(ctr)
	}
	var instances []Instance
	for range containers {
		r := <-ch
		if r.ok {
			instances = append(instances, r.inst)
		}
	}
	return instances, nil
}

// containerProbeScript combines the Claude presence check, proxy env probe,
// and cwd readlink into a single exec call to minimise Docker round-trips.
// Output format (one value per line):
//
//	<HUMAN_PROXY_ADDR or empty>
//	<cwd or empty>
const containerProbeScript = `pgrep -x claude >/dev/null 2>&1 || exit 1
printf '%s\n' "${HUMAN_PROXY_ADDR:-}"
readlink "/proc/$(pgrep -x claude)/cwd" 2>/dev/null || true`

// buildContainerInstance probes a single container for a running Claude process
// and returns an Instance if found.
func (d *DockerFinder) buildContainerInstance(ctx context.Context, ctr ContainerInfo, ttl time.Duration) (Instance, bool) {
	// Single exec: check Claude is running + collect proxy addr and cwd.
	exitCode, probeReader, err := d.Client.Exec(ctx, ctr.ID, []string{"sh", "-c", containerProbeScript})
	if err != nil || exitCode != 0 {
		return Instance{}, false
	}

	var proxyAddr, probedCwd string
	if probeReader != nil {
		if out, readErr := io.ReadAll(probeReader); readErr == nil {
			lines := strings.SplitN(strings.TrimRight(string(out), "\n"), "\n", 3)
			if len(lines) >= 1 {
				proxyAddr = strings.TrimSpace(lines[0])
			}
			if len(lines) >= 2 {
				probedCwd = strings.TrimSpace(lines[1])
			}
		}
	}

	data := d.getCached(ctr.ID, ttl)
	if data == nil {
		data = d.fetchContainerData(ctx, ctr.ID)
		if data == nil {
			return Instance{}, false
		}
		d.putCache(ctr.ID, data)
	}

	shortID := ctr.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	name := ctr.Name
	if name == "" {
		name = shortID
	}

	mem, memErr := d.Client.ContainerStats(ctx, ctr.ID)
	if memErr != nil {
		log.Debug().Err(memErr).Str("container", shortID).Msg("container stats unavailable")
	}

	// Prefer the host project path from the container label.
	cwd := ctr.Labels["dev.human.project"]
	if cwd == "" {
		cwd = probedCwd
	}

	return Instance{
		Label:           fmt.Sprintf("Container %q (%s)", name, shortID),
		Source:          "container",
		Walker:          &ByteWalker{Data: data},
		Root:            "/container/" + shortID,
		Memory:          mem,
		ContainerID:     ctr.ID,
		Cwd:             cwd,
		ProxyConfigured: proxyAddr != "",
	}, true
}

func (d *DockerFinder) fetchContainerData(ctx context.Context, containerID string) []byte {
	// List JSONL files with modification times from the container.
	_, listReader, err := d.Client.Exec(ctx, containerID, []string{
		"sh", "-c",
		"find /root/.claude/projects /home -maxdepth 6 -name '*.jsonl' -exec stat -c '%Y %n' {} + 2>/dev/null",
	})
	if err != nil {
		return nil
	}

	listData, err := io.ReadAll(listReader)
	if err != nil {
		return nil
	}

	sortedFiles := sortFilesByMtime(listData)
	if len(sortedFiles) == 0 {
		return nil
	}

	catArgs := append([]string{"cat"}, sortedFiles...)
	_, catReader, err := d.Client.Exec(ctx, containerID, catArgs)
	if err != nil {
		return nil
	}

	data, err := io.ReadAll(catReader)
	if err != nil {
		return nil
	}
	return data
}

func (d *DockerFinder) getCached(containerID string, ttl time.Duration) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil {
		return nil
	}
	entry, ok := d.cache[containerID]
	if !ok {
		return nil
	}
	if time.Since(entry.fetchedAt) > ttl {
		delete(d.cache, containerID)
		return nil
	}
	return entry.data
}

func (d *DockerFinder) putCache(containerID string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil {
		d.cache = make(map[string]*dockerCacheEntry)
	}
	d.cache[containerID] = &dockerCacheEntry{
		data:      data,
		fetchedAt: time.Now(),
	}
}

// sortFilesByMtime parses `stat -c '%Y %n'` output and returns file paths
// sorted by modification time (oldest first, newest last).
func sortFilesByMtime(data []byte) []string {
	type timedFile struct {
		mtime int64
		path  string
	}

	var files []timedFile
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte(" "), 2)
		if len(parts) != 2 {
			continue
		}
		mtime, err := strconv.ParseInt(string(parts[0]), 10, 64)
		if err != nil {
			continue
		}
		files = append(files, timedFile{mtime: mtime, path: string(parts[1])})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].mtime < files[j].mtime
	})

	result := make([]string, len(files))
	for i, f := range files {
		result[i] = f.path
	}
	return result
}

// CombinedFinder aggregates multiple InstanceFinders, logging and skipping failures.
type CombinedFinder struct {
	Finders []InstanceFinder
}

func (c *CombinedFinder) FindInstances(ctx context.Context) ([]Instance, error) {
	type result struct {
		instances []Instance
		err       error
	}
	ch := make(chan result, len(c.Finders))
	for _, f := range c.Finders {
		go func(f InstanceFinder) {
			instances, err := f.FindInstances(ctx)
			ch <- result{instances, err}
		}(f)
	}
	var all []Instance
	for range c.Finders {
		r := <-ch
		if r.err != nil {
			log.Debug().Err(r.err).Msg("instance finder failed, skipping")
			continue
		}
		all = append(all, r.instances...)
	}
	return all, nil
}

// DirWalker abstracts walking JSONL files for testability.
type DirWalker interface {
	WalkJSONL(root string, fn func(line []byte) error) error
}

// OSDirWalker implements DirWalker using the real filesystem.
type OSDirWalker struct{}

func (OSDirWalker) WalkJSONL(root string, fn func(line []byte) error) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		f, err := os.Open(filepath.Clean(path))
		if err != nil {
			return nil // skip unreadable files
		}
		defer func() { _ = f.Close() }()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
		for scanner.Scan() {
			if err := fn(scanner.Bytes()); err != nil {
				return err
			}
		}
		return nil
	})
}

// findNewestJSONL finds the most recently modified .jsonl file under root.
// When two files have mtimes within 1 second, the lexicographically later path
// wins as a stable tiebreaker (session IDs sort chronologically).
func findNewestJSONL(root string) (string, error) {
	var newest string
	var newestMod int64

	const mtimeTolerance = int64(time.Second)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error { // #nosec G703 -- root is the Claude config dir
		if err != nil {
			return nil // skip inaccessible
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		mod := info.ModTime().UnixNano()
		switch {
		case newestMod == 0:
			newestMod = mod
			newest = path
		case mod > newestMod+mtimeTolerance:
			// Clearly newer — pick it.
			newestMod = mod
			newest = path
		case mod >= newestMod-mtimeTolerance:
			// Within tolerance window — use lexicographic tiebreaker for stability.
			if path > newest {
				newestMod = mod
				newest = path
			}
		}
		return nil
	})
	return newest, err
}

// ByteWalker implements DirWalker over in-memory bytes (one JSONL line per text line).
type ByteWalker struct {
	Data []byte
}

func (b *ByteWalker) WalkJSONL(_ string, fn func(line []byte) error) error {
	scanner := bufio.NewScanner(bytes.NewReader(b.Data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return nil
}
