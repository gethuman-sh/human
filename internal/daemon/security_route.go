package daemon

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// SecurityCreateRequest is the security-create wire payload: the Security
// section's + dialog — a title and a free-text description. It mirrors
// BugCreateRequest; a distinct type keeps the two kinds free to diverge (a
// security ticket may later carry severity or a CVE reference).
type SecurityCreateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// SecurityCreateResponse reports the created security ticket.
type SecurityCreateResponse struct {
	Key string `json:"key"`
	URL string `json:"url,omitempty"`
}

// ValidateSecurityCreate rejects a request no tracker could accept. Trimming
// lives here so every transport (route, client, tests) agrees on "empty".
func ValidateSecurityCreate(req SecurityCreateRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		return errors.WithDetails("security title must not be empty")
	}
	return nil
}

// handleSecurityCreate files a security ticket on the PM tracker. One JSON arg,
// mirroring bug-create, so free-text titles and descriptions survive arg
// splitting.
func (s *Server) handleSecurityCreate(conn net.Conn, args []string) {
	if s.SecurityCreator == nil {
		s.writeError(conn, "security creation not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "security-create requires one JSON arg", 1)
		return
	}
	var req SecurityCreateRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid security-create request: "+err.Error(), 1)
		return
	}
	if err := ValidateSecurityCreate(req); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	created, err := s.SecurityCreator(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.pokeBoard()
	data, err := json.Marshal(created)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
