package daemon

import (
	"encoding/json"
	"net"
)

// RecreateDescriptionRequest is the recreate-description wire payload: the user
// chose "Recreate description" on one Product-Backlog card. The title travels
// with it for the same reason it does on a draft launch — a log line that names
// what is being rewritten; everything the drafter decides it decides from the
// ticket itself.
type RecreateDescriptionRequest struct {
	Key   string `json:"key"`
	Title string `json:"title,omitempty"`
}

// handleRecreateDescription launches the idea drafter against one ticket that
// is no longer an idea. It is the SAME producer the Ideas lane uses, in the
// same container under the same agent name; the only difference is the flag
// that tells the guard a person asked for this rewrite.
//
// Unlike handleIdeaCreate's fire-and-forget launch, this one is synchronous and
// its error is returned. Capture cannot fail usefully — the ticket exists
// either way — but a recreate that never starts must reach the board's error
// banner rather than looking like a rewrite that produced nothing.
func (s *Server) handleRecreateDescription(conn net.Conn, args []string) {
	if s.IdeaDraftLauncher == nil {
		s.writeError(conn, "description recreation not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "recreate-description requires one JSON arg", 1)
		return
	}
	var req RecreateDescriptionRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid recreate-description request: "+err.Error(), 1)
		return
	}
	draft := IdeaDraftRequest{Key: req.Key, Title: req.Title, Recreate: true}
	if err := ValidateIdeaDraft(draft); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	if err := s.IdeaDraftLauncher(draft); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.pokeBoard()
	resp := Response{Stdout: "ok\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
