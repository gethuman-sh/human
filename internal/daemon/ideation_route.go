package daemon

import (
	"encoding/json"
	"net"
)

// writeIdeationStatus marshals and writes the ideation status snapshot in the
// same shape all three ideation routes return.
func (s *Server) writeIdeationStatus(conn net.Conn, st IdeationStatus) {
	data, err := json.Marshal(st)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

// handleIdeationStart starts (or re-attaches to) the board ideation session.
// One JSON arg, mirroring board-transition, so free text survives arg splitting.
func (s *Server) handleIdeationStart(conn net.Conn, args []string) {
	if s.Ideation == nil {
		s.writeError(conn, "ideation not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "ideation-start requires one JSON arg", 1)
		return
	}
	var req IdeationStartRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid ideation-start request: "+err.Error(), 1)
		return
	}
	st, err := s.Ideation.Start(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeIdeationStatus(conn, st)
}

// handleIdeationReply forwards a user answer into the running session.
func (s *Server) handleIdeationReply(conn net.Conn, args []string) {
	if s.Ideation == nil {
		s.writeError(conn, "ideation not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "ideation-reply requires one JSON arg", 1)
		return
	}
	var req IdeationReplyRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid ideation-reply request: "+err.Error(), 1)
		return
	}
	st, err := s.Ideation.Reply(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeIdeationStatus(conn, st)
}

// handleIdeationStatus returns the current session snapshot; state "none"
// when no session exists so the client needs no error-path for the empty case.
func (s *Server) handleIdeationStatus(conn net.Conn) {
	if s.Ideation == nil {
		s.writeError(conn, "ideation not available", 1)
		return
	}
	s.writeIdeationStatus(conn, s.Ideation.Status())
}

// IdeaCreateRequest is the idea-create wire payload: a title-only quick
// capture from the board's Ideas column.
type IdeaCreateRequest struct {
	Title string `json:"title"`
}

// IdeaCreateResponse reports the created idea ticket.
type IdeaCreateResponse struct {
	Key string `json:"key"`
	URL string `json:"url,omitempty"`
}

// handleIdeaCreate quick-captures an idea-labeled ticket on the PM tracker.
// One JSON arg, mirroring ideation-start, so free-text titles survive arg
// splitting.
func (s *Server) handleIdeaCreate(conn net.Conn, args []string) {
	if s.Ideation == nil {
		s.writeError(conn, "ideation not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "idea-create requires one JSON arg", 1)
		return
	}
	var req IdeaCreateRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid idea-create request: "+err.Error(), 1)
		return
	}
	key, url, err := s.Ideation.CreateIdea(req.Title)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	// Capture IS the ask for a draft, and the ask must cost the user nothing:
	// the launch goes on a background goroutine so a slow container start never
	// delays the `+`. A nil launcher (no substrate wiring) simply skips it —
	// the same contract the bug-filing path's relate launch has.
	if s.IdeaDraftLauncher != nil {
		go func(key, title string) {
			_ = s.IdeaDraftLauncher(IdeaDraftRequest{Key: key, Title: title})
		}(key, req.Title)
	}
	data, err := json.Marshal(IdeaCreateResponse{Key: key, URL: url})
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
