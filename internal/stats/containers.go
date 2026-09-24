package stats

import (
	"context"
	"database/sql"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// Sample phases: a run row is a periodic reading while the container works; an
// exit row is the last reading the daemon took before it removed the container,
// and the only row that carries the exit code and the engine's OOM verdict.
const (
	PhaseRun  = "run"
	PhaseExit = "exit"
)

// ContainerSample is one reading of what an agent container cost the machine.
// MemLimit is the ceiling the engine enforces, so a row reads as "usage of
// limit" without a second lookup.
type ContainerSample struct {
	Timestamp   time.Time `json:"timestamp"`
	Project     string    `json:"project"`
	Agent       string    `json:"agent"`
	Key         string    `json:"key"`
	Stage       string    `json:"stage"`
	ContainerID string    `json:"container_id"`
	Phase       string    `json:"phase"`
	MemUsage    uint64    `json:"mem_usage"`
	MemLimit    uint64    `json:"mem_limit"`
	CPUPercent  float64   `json:"cpu_percent"`
	OnlineCPUs  int       `json:"online_cpus"`
	PIDs        uint64    `json:"pids"`
	// ExitCode is nil for a run row and for an exit row whose container could
	// not be inspected; a recorded zero is a real exit code.
	ExitCode  *int   `json:"exit_code,omitempty"`
	OOMKilled bool   `json:"oom_killed"`
	Reason    string `json:"reason,omitempty"`
}

// StageResources rolls up the samples of one board stage over a range.
type StageResources struct {
	Stage          string  `json:"stage"`
	Runs           int     `json:"runs"`
	Samples        int     `json:"samples"`
	PeakMemBytes   uint64  `json:"peak_mem_bytes"`
	MemLimitBytes  uint64  `json:"mem_limit_bytes"`
	AvgCPUPercent  float64 `json:"avg_cpu_percent"`
	PeakCPUPercent float64 `json:"peak_cpu_percent"`
	OOMKills       int     `json:"oom_kills"`
}

func (s *StatsStore) ensureContainerSchema() error {
	const schema = `
		CREATE TABLE IF NOT EXISTS container_samples (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp    DATETIME NOT NULL,
			project      TEXT NOT NULL DEFAULT '',
			agent        TEXT NOT NULL DEFAULT '',
			key          TEXT NOT NULL DEFAULT '',
			stage        TEXT NOT NULL DEFAULT '',
			container_id TEXT NOT NULL DEFAULT '',
			phase        TEXT NOT NULL DEFAULT 'run',
			mem_usage    INTEGER NOT NULL DEFAULT 0,
			mem_limit    INTEGER NOT NULL DEFAULT 0,
			cpu_percent  REAL NOT NULL DEFAULT 0,
			online_cpus  INTEGER NOT NULL DEFAULT 0,
			pids         INTEGER NOT NULL DEFAULT 0,
			exit_code    INTEGER,
			oom_killed   INTEGER NOT NULL DEFAULT 0,
			reason       TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_container_samples_timestamp
			ON container_samples (timestamp);
		CREATE INDEX IF NOT EXISTS idx_container_samples_agent
			ON container_samples (agent, timestamp);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return errors.WrapWithDetails(err, "create container samples schema")
	}
	return nil
}

// InsertContainerSample persists one reading.
func (s *StatsStore) InsertContainerSample(ctx context.Context, c ContainerSample) error {
	ts := c.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	phase := c.Phase
	if phase == "" {
		phase = PhaseRun
	}
	var exit sql.NullInt64
	if c.ExitCode != nil {
		exit = sql.NullInt64{Int64: int64(*c.ExitCode), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO container_samples
			(timestamp, project, agent, key, stage, container_id, phase,
			 mem_usage, mem_limit, cpu_percent, online_cpus, pids, exit_code, oom_killed, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ts.UTC().Format("2006-01-02 15:04:05"), c.Project, c.Agent, c.Key, c.Stage, c.ContainerID, phase,
		int64(c.MemUsage), int64(c.MemLimit), c.CPUPercent, c.OnlineCPUs, int64(c.PIDs), exit, c.OOMKilled, c.Reason) // #nosec G115 -- byte counts fit int64
	if err != nil {
		return errors.WrapWithDetails(err, "insert container sample", "agent", c.Agent, "phase", phase)
	}
	return nil
}

// QueryContainerResources rolls the range's samples up per stage. Runs counts
// distinct agents, so a stage that sampled one long run fifty times reads as
// one run, not fifty.
func (s *StatsStore) QueryContainerResources(ctx context.Context, since, until time.Time) ([]StageResources, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stage,
		       COUNT(DISTINCT agent),
		       COUNT(*),
		       COALESCE(MAX(mem_usage), 0),
		       COALESCE(MAX(mem_limit), 0),
		       COALESCE(AVG(cpu_percent), 0),
		       COALESCE(MAX(cpu_percent), 0),
		       COALESCE(SUM(oom_killed), 0)
		FROM container_samples
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY stage
		ORDER BY stage ASC
	`, since.UTC().Format("2006-01-02 15:04:05"), until.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "query container resources")
	}
	defer func() { _ = rows.Close() }()

	var result []StageResources
	for rows.Next() {
		var r StageResources
		var peakMem, limit int64
		if err := rows.Scan(&r.Stage, &r.Runs, &r.Samples, &peakMem, &limit, &r.AvgCPUPercent, &r.PeakCPUPercent, &r.OOMKills); err != nil {
			return nil, errors.WrapWithDetails(err, "scan container resources")
		}
		r.PeakMemBytes = uint64(peakMem) // #nosec G115 -- stored from a uint64
		r.MemLimitBytes = uint64(limit)  // #nosec G115 -- stored from a uint64
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *StatsStore) pruneContainerSamples(ctx context.Context, cutoff string) (int64, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM container_samples WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, errors.WrapWithDetails(err, "prune container samples")
	}
	return result.RowsAffected()
}
