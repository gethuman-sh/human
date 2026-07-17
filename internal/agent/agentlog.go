package agent

// Host-side per-execution log store. A container-based agent run that dies —
// failed, or reaped by the zombie sweep — must stay analyzable afterwards: the
// detached stdout, the Claude session transcript, and the outcome all vanish
// with the container unless they are teed and copied out to the host first.
// This store is the durable record every remove path writes into, keyed by
// agent name and listed newest-first, mirroring the audit trail's UX.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/claude/hookevents"
	"github.com/gethuman-sh/human/internal/devcontainer"
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
	_ = CopyTranscript(ctx, docker, meta.ContainerID, meta.RemoteUser, exe.TranscriptDir())
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
