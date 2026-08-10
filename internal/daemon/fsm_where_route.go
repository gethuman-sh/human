package daemon

import (
	"encoding/json"
	"net"
	"strings"
	"time"

	"github.com/gethuman-sh/human/internal/pipelinefsm"
	"github.com/gethuman-sh/human/internal/tracker"
)

// WhereRequest is the fsm-where wire payload.
type WhereRequest struct {
	Key string `json:"key"`
	// Actor is who is asking. Only this actor's ways out come back with a
	// runnable command, so defaulting matters: `skill` owns the fewest edges, and
	// guessing it withholds a command rather than offering one the caller may not
	// use.
	Actor string `json:"actor,omitempty"`
	// History bounds the trail of past positions. Zero means the default;
	// negative means none, for a caller that only wants the position.
	History int `json:"history,omitempty"`
}

// WhereCommentReader loads one ticket's comment thread and status. Injected so
// the route stays a shaping step and the tracker fan-out stays where every
// other route already keeps it.
type WhereCommentReader func(key string) (comments []tracker.Comment, status tracker.Category, isIdea bool, err error)

// handleFSMWhere answers where one ticket is. One JSON arg, mirroring the other
// routes.
func (s *Server) handleFSMWhere(conn net.Conn, args []string) {
	if s.WhereComments == nil {
		s.writeError(conn, "the daemon cannot read this project's tickets, so it cannot say where one is", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "fsm-where requires one JSON arg", 1)
		return
	}
	var req WhereRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid fsm-where request: "+err.Error(), 1)
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		s.writeError(conn, "fsm-where needs a ticket key", 1)
		return
	}
	actor := req.Actor
	if actor == "" {
		actor = "skill"
	}

	doc, err := pipelinefsm.Load()
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	if _, known := doc.Actors[actor]; !known {
		s.writeError(conn, "no such actor in the pipeline state machine: "+actor, 1)
		return
	}

	comments, status, isIdea, err := s.WhereComments(req.Key)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}

	report := BuildWhere(doc, req.Key, comments, status, isIdea, actor, WhereDeps{
		Progress:     s.whereProgress(),
		Attempts:     s.WhereAttempts,
		HistoryLimit: req.History,
		Now:          time.Now(),
	})
	data, err := json.Marshal(report)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	_ = json.NewEncoder(conn).Encode(resp)
}

// whereProgress hands back the daemon's ONE progress probe — the same value the
// zombie sweep and the reconcile pass judge liveness with. It used to hand back
// the raw hook-store probe, which folds in nothing from the proxy, so
// `human fsm where` reported outstanding_model_request: false for every agent
// alive and computed `stalled` on the 3-minute budget for a working one
// (SC-3853). nil when nothing is wired: an answer without a liveness section
// beats an answer that invents one.
func (s *Server) whereProgress() AgentProgressProbe {
	return s.AgentProgress
}
