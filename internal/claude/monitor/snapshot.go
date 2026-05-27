package monitor

import (
	"time"

	"github.com/gethuman-sh/human/internal/claude"
	"github.com/gethuman-sh/human/internal/claude/logparser"
	"github.com/gethuman-sh/human/internal/daemon"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

// Snapshot holds the complete TUI display state at a point in time.
type Snapshot struct {
	FetchedAt  time.Time
	Err        error
	Daemon     DaemonState
	Telegram   string
	Slack      string
	Trackers   []tracker.TrackerStatus
	Instances  []InstanceView
	Panes      []claude.TmuxPane
	TotalUsage *claude.UsageSummary

	// NetworkEvents is the deduplicated ambient network activity list
	// from the daemon store, in insertion order (oldest first). The
	// renderer reverses this for newest-on-top display.
	NetworkEvents []daemon.NetworkEvent

	// ToolStats holds pre-aggregated historical tool call statistics
	// fetched from the daemon's SQLite store. Populated on fullTick only.
	ToolStats *stats.ToolStats

	// internal — carried forward between fetches, not used by renderers.
	sessionByPath map[string]logparser.SessionState
	connectedPIDs map[int]bool // PIDs of Claude instances connected to the daemon
}

// DaemonState holds the daemon liveness info.
type DaemonState struct {
	PID              int
	Alive            bool
	ProxyAddr        string // proxy listen address from daemon.json
	ProxyActiveConns int64  // number of currently active proxy connections
}

// InstanceView pairs a discovered instance with its matched session (if any).
type InstanceView struct {
	Usage   claude.InstanceUsage
	Session *logparser.SessionState // nil if no session matched
}
