package agent

import (
	"os"
	"time"

	"github.com/gethuman-sh/human/errors"

	"github.com/gethuman-sh/human/internal/agentname"
)

// StoppedBoardAgents reads the agents this machine has recorded as no longer
// running, with the moment each stop was written. Two sources feed it, because
// no single one survives every path an agent stops through:
//
//   - the execution log's outcome.json, written by PreserveExecutionArtifacts
//     BEFORE the meta is deleted. That covers `human agent stop`
//     (Manager.Stop) AND the automatic zombie sweep's kill/OOM/crash reap
//     (Manager.Delete = stopLocked + DeleteMeta) — the case the meta alone
//     cannot show, because DeleteMeta erases the very meta stopLocked just
//     wrote, in the same call (SC-5327).
//   - the agent meta's StoppedAt, written by Manager.Refresh (only caller:
//     `human agent list`, which writes StatusStopped straight to the meta and
//     never touches the execution log) — the one producer the log does not
//     capture.
//
// Where both name the same agent, the execution log's EndedAt wins: it is
// written at the one choke point every remove path funnels through, so it is
// the earlier and more authoritative record of when the run actually ended.
//
// Read both by the daemon's stuck-running shortcut (recordedDeath, SC-5327)
// and by the desktop's board liveness overlay (SC-5091).
func StoppedBoardAgents() (map[string]time.Time, error) {
	metas, err := ListMetas()
	if err != nil {
		return nil, err
	}
	stopped := stoppedBoardAgentsFromMeta(metas)
	reaped, err := reapedBoardAgentsFromExecutionLog()
	if err != nil {
		// A broken execution-log read must not blind the meta-based half —
		// return what the meta already told us rather than nothing.
		return stopped, nil
	}
	// A stale execution-log entry (e.g. a launch whose NewExecution failed
	// and left a prior run's outcome.json as the newest one on disk) must
	// never overrule a meta that says the agent is running right now —
	// otherwise a currently-live agent gets reported stopped (SC-5327).
	running := runningBoardAgentNames(metas)
	for name, at := range reaped {
		if _, alive := running[name]; alive {
			continue
		}
		stopped[name] = at
	}
	return stopped, nil
}

// stoppedBoardAgentsFromMeta is the meta-only half of StoppedBoardAgents: the
// producers that write StatusStopped straight to the meta and leave it there.
func stoppedBoardAgentsFromMeta(metas []Meta) map[string]time.Time {
	stopped := make(map[string]time.Time, len(metas))
	for _, m := range metas {
		if m.Status == StatusRunning || m.StoppedAt.IsZero() {
			continue
		}
		stopped[m.Name] = m.StoppedAt
	}
	return stopped
}

// runningBoardAgentNames returns the names whose meta currently says
// StatusRunning, so the execution-log half of StoppedBoardAgents can never
// report a live agent as stopped on the strength of a stale prior run's
// outcome.json.
func runningBoardAgentNames(metas []Meta) map[string]struct{} {
	running := make(map[string]struct{})
	for _, m := range metas {
		if m.Status == StatusRunning {
			running[m.Name] = struct{}{}
		}
	}
	return running
}

// reapedBoardAgentsFromExecutionLog reads the newest execution-log outcome for
// every board agent this host holds a log for, keyed by agent name. Only board
// agents are worth the scan: recordedDeath only ever looks up a
// board-<key>-<stage> name, and skipping the rest avoids reading every
// interactive agent's log on a machine that runs both. A directory with no
// outcome.json (a run still in flight) or one with neither disposition
// recorded is not evidence and is skipped.
func reapedBoardAgentsFromExecutionLog() (map[string]time.Time, error) {
	root := ExecutionLogsDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.WrapWithDetails(err, "listing execution logs", "dir", root)
	}
	out := make(map[string]time.Time)
	for _, e := range entries {
		if !e.IsDir() || !agentname.IsBoard(e.Name()) {
			continue
		}
		execs, err := ListExecutions(e.Name())
		if err != nil || len(execs) == 0 {
			continue
		}
		outcome := execs[0].Outcome
		if outcome == nil || outcome.Disposition == "" || outcome.EndedAt.IsZero() {
			continue
		}
		out[e.Name()] = outcome.EndedAt
	}
	return out, nil
}
