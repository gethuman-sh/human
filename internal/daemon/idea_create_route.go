package daemon

import (
	"encoding/json"
	"net"
)

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
// One JSON arg, mirroring board-transition, so free-text titles survive arg
// splitting.
func (s *Server) handleIdeaCreate(conn net.Conn, args []string) {
	if s.IdeaCreator == nil {
		s.writeError(conn, "idea creation not available", 1)
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
	key, url, err := s.IdeaCreator.CreateIdea(req.Title)
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
