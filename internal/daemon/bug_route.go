package daemon

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// BugCreateRequest is the bug-create wire payload: the Bugs pane's + dialog —
// a title and a free-text description.
type BugCreateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// BugCreateResponse reports the created bug ticket.
type BugCreateResponse struct {
	Key string `json:"key"`
	URL string `json:"url,omitempty"`
}

// ValidateBugCreate rejects a request no tracker could accept. Trimming lives
// here so every transport (route, client, tests) agrees on what "empty" means.
func ValidateBugCreate(req BugCreateRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		return errors.WithDetails("bug title must not be empty")
	}
	return nil
}

// handleBugCreate files a defect ticket on the PM tracker. One JSON arg,
// mirroring idea-create, so free-text titles and descriptions survive arg
// splitting.
func (s *Server) handleBugCreate(conn net.Conn, args []string) {
	if s.BugCreator == nil {
		s.writeError(conn, "bug creation not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "bug-create requires one JSON arg", 1)
		return
	}
	var req BugCreateRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid bug-create request: "+err.Error(), 1)
		return
	}
	if err := ValidateBugCreate(req); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	created, err := s.BugCreator(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	data, err := json.Marshal(created)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
