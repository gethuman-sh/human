package daemon

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// IdeaPromoteRequest promotes one idea to a PM ticket. Labels is the card's
// full label set; only the idea-classifying ones come off, so promotion never
// strips a label that meant something else.
type IdeaPromoteRequest struct {
	Key    string   `json:"key"`
	Labels []string `json:"labels,omitempty"`
}

// ValidateIdeaPromote rejects a request naming no ticket.
func ValidateIdeaPromote(req IdeaPromoteRequest) error {
	if strings.TrimSpace(req.Key) == "" {
		return errors.WithDetails("idea promote request needs a key", "key", req.Key)
	}
	return nil
}

// IdeaLabelsToRemove keeps only the idea-classifying labels from a card's set,
// falling back to the canonical pair when the caller sent none (removing an
// absent label is a no-op, so the fallback is safe).
//
// Lifted out of the ideation engine's terminal action, which SC-4520 retired
// along with the whole agent-driven promotion path: this route is the only
// thing that strips an idea's labels now, so the rule has one home and one
// caller.
func IdeaLabelsToRemove(labels []string) []string {
	var idea []string
	for _, l := range labels {
		if (tracker.Issue{Labels: []string{l}}).IsIdea() {
			idea = append(idea, l)
		}
	}
	if len(idea) == 0 {
		return []string{tracker.IdeaLabel, "idea"}
	}
	return idea
}

// handleIdeaPromote graduates an idea by removing its idea labels and nothing
// else. No agent turn and no marker: the ticket keeps its key, its title and
// its description, and the description editor the board opens next is where the
// conversation happens.
func (s *Server) handleIdeaPromote(conn net.Conn, args []string) {
	if s.IdeaPromoter == nil {
		s.writeError(conn, "idea promotion not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "idea-promote requires one JSON arg", 1)
		return
	}
	var req IdeaPromoteRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid idea-promote request: "+err.Error(), 1)
		return
	}
	if err := ValidateIdeaPromote(req); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	if err := s.IdeaPromoter(req); err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	// The card leaves Ideas on the next refetch: the label edit happened on the
	// tracker, and nothing else tells the board about it.
	s.pokeBoard()
	enc := json.NewEncoder(conn)
	_ = enc.Encode(Response{})
}
