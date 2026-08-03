package daemon

import (
	"context"
	"encoding/json"
	"net"
)

// DaemonBusyStatus is the wire form of the daemon-busy route: whether the
// project this daemon serves currently has a live stage lease. This is only
// half of the desktop close flow's "would stopping the daemon kill in-flight
// work" question — the other half (a live Claude Code instance with status
// "working") is discovered client-side, in-process, the same way the Agents
// view already does (SC-3015).
type DaemonBusyStatus struct {
	Busy bool `json:"busy"`
}

// handleDaemonBusy answers the daemon-busy route. A nil LeaseChecker (an
// older build, or a daemon started without agent-state support) reports
// Busy: false rather than failing the request — the close flow must never be
// unable to close just because this one signal is unavailable.
func (s *Server) handleDaemonBusy(conn net.Conn) {
	busy := false
	if s.LeaseChecker != nil {
		var err error
		busy, err = s.LeaseChecker(context.Background(), s.defaultProjectName())
		if err != nil {
			s.writeError(conn, err.Error(), 1)
			return
		}
	}
	data, err := json.Marshal(DaemonBusyStatus{Busy: busy})
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// defaultProjectName names the project a whole-daemon (not per-ticket) check
// applies to, mirroring resolveStateProject's "fewer than two registered
// projects resolve to the default "" rule — so a single-project desktop
// install's lease writes and this read agree on one namespace without
// guessing (SC-3015, following the project-scoping convention SC-2326 set).
func (s *Server) defaultProjectName() string {
	if s.Projects == nil || len(s.Projects.Entries()) < 2 {
		return ""
	}
	return s.Projects.Entries()[0].Name
}
