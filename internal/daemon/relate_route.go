package daemon

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// RelateRequest is the related-work-triage launch payload: the PM key of the
// bug to triage, and its title carried alongside so a downstream launch needs no
// refetch to name the card.
type RelateRequest struct {
	PMKey   string `json:"pm_key"`
	PMTitle string `json:"pm_title,omitempty"`
}

// ValidateRelate rejects a request no launch could act on. Trimming lives here
// so every transport (route, client, the auto-launch after filing) agrees on
// what "empty" means.
func ValidateRelate(req RelateRequest) error {
	if strings.TrimSpace(req.PMKey) == "" {
		return errors.WithDetails("relate request needs a pm_key")
	}
	return nil
}

// handleRelate launches the filing-time related-work triage on one bug via the
// injected RelateLauncher. Like features-generate it is a dedicated
// non-destructive route; the card-menu click is the user's consent.
func (s *Server) handleRelate(conn net.Conn, args []string) {
	if s.RelateLauncher == nil {
		s.writeError(conn, "related-work triage not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "relate requires one JSON arg", 1)
		return
	}
	var req RelateRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid relate request: "+err.Error(), 1)
		return
	}
	if err := ValidateRelate(req); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	if err := s.RelateLauncher(req); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(Response{})
}
