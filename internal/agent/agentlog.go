package agent

// Host-side per-execution log store. A container-based agent run that dies —
// failed, or reaped by the zombie sweep — must stay analyzable afterwards: the
// detached stdout, the Claude session transcript, and the outcome all vanish
// with the container unless they are teed and copied out to the host first.
// This store is the durable record every remove path writes into, keyed by
// agent name and listed newest-first, mirroring the audit trail's UX.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/gethuman-sh/human/internal/gitrepo"
)

// execRetentionDays is the rolling window for execution-log retention. Matches
// audit.RetentionDays: these are accountability records that must outlive the
// short-lived trend graphs.
const execRetentionDays = 90

// execIDBytes is the number of random bytes hex-encoded into an execution id.
// 16 bytes yields a 32-char hex string with ample uniqueness. Mirrors the
// crypto/rand pattern in internal/audit/event.go so no UUID dependency is
// pulled into the dependency-light agent package.
const execIDBytes = 16

// agentLogsDirOverride lets tests redirect the log root. Empty = default.
var agentLogsDirOverride string

// ExecutionLogsDir returns ~/.human/agent-logs (falls back to ./.human/agent-logs
// when the home directory is unknown), sitting beside the agents metadata dir.
func ExecutionLogsDir() string {
	if agentLogsDirOverride != "" {
		return agentLogsDirOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".human", "agent-logs")
	}
	return filepath.Join(home, ".human", "agent-logs")
}

// newExecID returns a cryptographically random 32-char hex execution id.
func newExecID() string {
	b := make([]byte, execIDBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is effectively fatal for the process; fall back to
		// a timestamp-derived id so the run still gets a distinct directory.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}

// LaunchRecord is written before detach: the exact launch of a claude run.
type LaunchRecord struct {
	ID          string    `json:"id"`
	Agent       string    `json:"agent"`
	Prompt      string    `json:"prompt"`
	Argv        []string  `json:"argv"`
	Model       string    `json:"model,omitempty"`
	ContainerID string    `json:"container_id"`
	StartedAt   time.Time `json:"started_at"`
	// Worktree is the per-run private worktree; the retention prune sweeps a
	// KEPT (reaped-run) worktree when it drops the execution dir.
	Worktree string `json:"worktree,omitempty"`
	// RepoDir is the shared repo the worktree was cut from — the `git -C <repo>
	// worktree remove <worktree>` needs the parent repo as its first operand.
	// Retention persisted it so the sweep can detach a kept worktree instead of
	// passing the worktree as both operands (a no-op that leaked the tree).
	RepoDir string `json:"repo_dir,omitempty"`
}

// OutcomeRecord is written on completion/reap: why and when a run ended.
type OutcomeRecord struct {
	Reason     string    `json:"reason"` // "completed" | "failed" | "reaped"
	ExitCode   int       `json:"exit_code"`
	DurationMs int64     `json:"duration_ms"`
	Result     string    `json:"result,omitempty"`
	EndedAt    time.Time `json:"ended_at"`
}

// Execution is the on-disk root for one run: <logsDir>/<agent>/<id>/.
type Execution struct {
	dir    string
	Launch LaunchRecord
}

// executionDir returns the run directory for an agent/id pair.
func executionDir(agentName, id string) string {
	return filepath.Join(ExecutionLogsDir(), agentName, id)
}

// NewExecution creates the run directory and writes launch.json. The agent name
// is validated at Start, so a plain join is safe; guard defensively anyway.
func NewExecution(lr LaunchRecord) (*Execution, error) {
	if !isValidName(lr.Agent) {
		return nil, errors.WithDetails("invalid agent name for execution log", "name", lr.Agent)
	}
	dir := executionDir(lr.Agent, lr.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.WrapWithDetails(err, "creating execution log directory", "dir", dir)
	}
	e := &Execution{dir: dir, Launch: lr}
	if err := writeJSONFile(filepath.Join(dir, "launch.json"), lr); err != nil {
		return nil, err
	}
	return e, nil
}

// Dir returns the on-disk root for this execution.
func (e *Execution) Dir() string { return e.dir }

// OutputWriter returns an append writer to <dir>/output.log (0600), created on
// first call. The detached exec's demuxed stdout/stderr is teed here.
func (e *Execution) OutputWriter() (io.WriteCloser, error) {
	path := filepath.Join(e.dir, "output.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- path derived from validated agent name + hex id
	if err != nil {
		return nil, errors.WrapWithDetails(err, "opening execution output log", "path", path)
	}
	return f, nil
}

// TranscriptDir returns <dir>/transcript, the copy-out target for the Claude
// session transcript. Created lazily by the copy-out.
func (e *Execution) TranscriptDir() string {
	return filepath.Join(e.dir, "transcript")
}

// RecordOutcome writes outcome.json.
func (e *Execution) RecordOutcome(o OutcomeRecord) error {
	return writeJSONFile(filepath.Join(e.dir, "outcome.json"), o)
}

// HasOutcome reports whether outcome.json already exists for this execution.
// PreserveExecutionArtifacts (the teardown path) is the authoritative writer
// and always runs before the container is destroyed; recordExecOutcome (the
// tee, at stream EOF) checks this first so it never clobbers a
// teardown-written classification like "reaped" (SC-1688) — the tee is the
// sole writer only in the no-teardown case, where no file exists yet.
func (e *Execution) HasOutcome() bool {
	_, err := os.Stat(filepath.Join(e.dir, "outcome.json"))
	return err == nil
}

// ExecutionSummary is one run as surfaced to `human agent logs`.
type ExecutionSummary struct {
	Launch  LaunchRecord   `json:"launch"`
	Outcome *OutcomeRecord `json:"outcome,omitempty"`
	Dir     string         `json:"dir"`
}

// ListExecutions returns all executions for an agent, newest-first by
// StartedAt, attaching outcome.json when present.
func ListExecutions(agentName string) ([]ExecutionSummary, error) {
	root := filepath.Join(ExecutionLogsDir(), agentName)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.WrapWithDetails(err, "listing execution logs", "dir", root)
	}
	var out []ExecutionSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		var lr LaunchRecord
		if err := readJSONFile(filepath.Join(dir, "launch.json"), &lr); err != nil {
			continue // skip incomplete/corrupt runs
		}
		sum := ExecutionSummary{Launch: lr, Dir: dir}
		var oc OutcomeRecord
		if err := readJSONFile(filepath.Join(dir, "outcome.json"), &oc); err == nil {
			sum.Outcome = &oc
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Launch.StartedAt.After(out[j].Launch.StartedAt)
	})
	return out, nil
}

// LatestExecution returns the newest execution for an agent, so a remove path
// with no execution id on the meta can still find the current run's dir.
func LatestExecution(agentName string) (*Execution, error) {
	execs, err := ListExecutions(agentName)
	if err != nil {
		return nil, err
	}
	if len(execs) == 0 {
		return nil, errors.WithDetails("no executions for agent", "name", agentName)
	}
	return &Execution{dir: execs[0].Dir, Launch: execs[0].Launch}, nil
}

// lookupExecution resolves the execution dir for a meta: prefer the exact id
// recorded at launch, else fall back to the agent's latest run. Returns nil
// when nothing is found — callers treat a missing execution as non-fatal.
func lookupExecution(meta Meta) *Execution {
	if meta.ExecutionID != "" {
		dir := executionDir(meta.Name, meta.ExecutionID)
		var lr LaunchRecord
		if err := readJSONFile(filepath.Join(dir, "launch.json"), &lr); err == nil {
			return &Execution{dir: dir, Launch: lr}
		}
	}
	exe, err := LatestExecution(meta.Name)
	if err != nil {
		return nil
	}
	return exe
}

// PreserveExecutionArtifacts copies the run's transcript out of the container
// and records the outcome — the last chance to capture both before a
// force-remove destroys them. Best-effort by contract: teardown must proceed
// whether or not anything could be preserved, so failures are swallowed. Every
// remove path (Manager stop/delete and the daemon's async decommission bypass)
// funnels through this one preservation step.
func PreserveExecutionArtifacts(ctx context.Context, docker devcontainer.DockerClient, meta Meta, reason string) {
	exe := lookupExecution(meta)
	if exe == nil {
		return
	}
	_ = CopyTranscript(ctx, docker, meta.ContainerID, meta.RemoteUser, exe.Launch.Worktree, exe.TranscriptDir())
	_ = exe.RecordOutcome(OutcomeRecord{
		Reason: reason, EndedAt: time.Now(),
		DurationMs: time.Since(meta.CreatedAt).Milliseconds(),
	})
}

// stopReason classifies why a run is ending at the remove choke point. The
// zombie sweep marks reaped agents with StatusFailed; a plain stop is a
// completion.
func stopReason(meta Meta) string {
	if meta.Status == StatusFailed {
		return "reaped"
	}
	return "completed"
}

// PruneExecutions deletes execution dirs whose launch is older than
// execRetentionDays. Mirrors audit.Prune. Returns the number of runs removed.
func PruneExecutions() (int, error) {
	root := ExecutionLogsDir()
	agents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, errors.WrapWithDetails(err, "listing execution log root", "dir", root)
	}
	cutoff := time.Now().Add(-execRetentionDays * 24 * time.Hour)
	removed := 0
	for _, a := range agents {
		if !a.IsDir() {
			continue
		}
		agentRoot := filepath.Join(root, a.Name())
		runs, err := os.ReadDir(agentRoot)
		if err != nil {
			continue
		}
		for _, r := range runs {
			if !r.IsDir() {
				continue
			}
			dir := filepath.Join(agentRoot, r.Name())
			var lr LaunchRecord
			if err := readJSONFile(filepath.Join(dir, "launch.json"), &lr); err != nil {
				continue
			}
			if lr.StartedAt.Before(cutoff) {
				// A reaped run's worktree was KEPT for forensics; retention is
				// its final sweep. Detach it from the shared repo before dropping
				// the tree, best-effort — retention must never fail on a stale
				// worktree (repo may be gone). The parent repo is the first
				// operand; passing the worktree as both (the earlier defect) is a
				// no-op that left the tree registered.
				if lr.Worktree != "" {
					_ = gitrepo.WorktreeRemove(context.Background(), lr.RepoDir, lr.Worktree)
					_ = os.RemoveAll(lr.Worktree)
				}
				if err := os.RemoveAll(dir); err == nil {
					removed++
				}
			}
		}
	}
	return removed, nil
}

// HookEventSink appends a hook event as one JSON line to the agent's latest
// execution dir (<logsDir>/<agent>/<latest-id>/hooks.jsonl). It is best-effort
// and never surfaces an error into the daemon's hot path: a hook event tied to
// an agent must survive the in-memory ring's eviction and daemon restarts. A
// missing agent name (non-agent session) or no known execution is a no-op.
func HookEventSink(evt hookevents.Event) {
	if evt.AgentName == "" || !isValidName(evt.AgentName) {
		return
	}
	exe, err := LatestExecution(evt.AgentName)
	if err != nil {
		return
	}
	line, err := json.Marshal(evt)
	if err != nil {
		return
	}
	path := filepath.Join(exe.Dir(), "hooks.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- path derived from validated agent name + hex id
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// LatestOutputPath returns the output.log path of the agent's newest execution,
// erroring when the agent has no recorded runs.
func LatestOutputPath(agentName string) (string, error) {
	exe, err := LatestExecution(agentName)
	if err != nil {
		return "", err
	}
	return filepath.Join(exe.Dir(), "output.log"), nil
}

// LatestSessionID returns the Claude session the agent's newest run is writing,
// read from the run's own hook trail — every event carries agent_name and
// session_id together, so the pairing is already recorded and needs no probe.
//
// It exists because agent containers share one transcript directory: every
// board agent mounts the same .devcontainer/claude as ~/.claude, so a container
// asked what it has been doing answers with every agent's transcripts at once.
// Without this, four containers reported one identical token figure and one
// container's session state could be read out of another's file (SC-4151 C6).
//
// An agent with no runs, an unreadable trail, or a trail with no session yet
// returns "" — the caller then leaves its behaviour unnarrowed rather than
// guessing which session is the agent's.
func LatestSessionID(agentName string) string {
	exe, err := LatestExecution(agentName)
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(exe.Dir(), "hooks.jsonl")) // #nosec G304 -- path derived from validated agent name + hex id
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var evt hookevents.Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			continue
		}
		if evt.SessionID != "" {
			return evt.SessionID
		}
	}
	return ""
}

// SessionForContainer is LatestSessionID keyed by CONTAINER name, which is what
// instance discovery holds. It is the value to hand to
// claude.DockerFinder.SessionForContainer; a container this package did not
// name is not a managed agent and resolves to "".
func SessionForContainer(containerName string) string {
	name, ok := strings.CutPrefix(strings.TrimSpace(containerName), ContainerPrefix)
	if !ok || name == "" {
		return ""
	}
	return LatestSessionID(name)
}

// StreamOutput writes the contents of the file at path to w. When follow is
// true it keeps polling for appended bytes (every pollInterval) and writes them
// until ctx is cancelled. tailLines > 0 starts the stream at the last N lines of
// existing content instead of the whole file.
func StreamOutput(ctx context.Context, w io.Writer, path string, follow bool, tailLines int) error {
	f, err := os.Open(path) // #nosec G304 -- path derived from validated agent name + hex id
	if err != nil {
		return errors.WrapWithDetails(err, "opening agent output log", "path", path)
	}
	defer func() { _ = f.Close() }()

	if tailLines > 0 {
		if err := seekToLastLines(f, tailLines); err != nil {
			return err
		}
	}

	// io.Copy from a *os.File preserves the file offset across calls, so after it
	// reaches EOF a later io.Copy on the same handle picks up appended bytes —
	// this is the tail-f loop. (Regular files only; the log is never rotated.)
	if _, err := io.Copy(w, f); err != nil {
		return errors.WrapWithDetails(err, "reading agent output log", "path", path)
	}
	if !follow {
		return nil
	}

	const pollInterval = 400 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := io.Copy(w, f); err != nil {
				return errors.WrapWithDetails(err, "tailing agent output log", "path", path)
			}
		}
	}
}

// seekToLastLines positions f at the start of its final n lines (or the file
// start if it has fewer). Scans from the end in fixed-size chunks counting
// newlines; a whole-file scan is unnecessary for a rolling tail. Note ReadAt
// does not move the file offset, so the final Seek sets the streaming cursor.
func seekToLastLines(f *os.File, n int) error {
	stat, err := f.Stat()
	if err != nil {
		return errors.WrapWithDetails(err, "stat agent output log")
	}
	const chunk = 4096
	size := stat.Size()
	var (
		newlines int
		offset   = size
		buf      = make([]byte, chunk)
	)
	for offset > 0 {
		readSize := int64(chunk)
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize
		if _, err := f.ReadAt(buf[:readSize], offset); err != nil && err != io.EOF {
			return errors.WrapWithDetails(err, "scanning agent output log tail")
		}
		for i := int(readSize) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				newlines++
				// n newlines below the top means n complete trailing lines start
				// just after this newline.
				if newlines > n {
					_, err := f.Seek(offset+int64(i)+1, io.SeekStart)
					return err
				}
			}
		}
	}
	_, err = f.Seek(0, io.SeekStart)
	return err
}

// writeJSONFile writes v as indented JSON with 0600 permissions, matching
// WriteMeta.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling execution log record", "path", path)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errors.WrapWithDetails(err, "writing execution log record", "path", path)
	}
	return nil
}

// readJSONFile decodes the JSON file at path into v.
func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path derived from validated agent name + hex id
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
