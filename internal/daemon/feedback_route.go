package daemon

import (
	"encoding/json"
	"net"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// FeedbackRequest is the feedback wire payload: which ticket, which stage,
// and — for the pull-request stages, whose scope is a branch diff — which
// branch. Raw asks for the prompt the block was distilled from.
type FeedbackRequest struct {
	Key    string     `json:"key"`
	Stage  BoardStage `json:"stage"`
	Branch string     `json:"branch,omitempty"`
	Raw    bool       `json:"raw,omitempty"`
}

// FeedbackReport is what `human feedback` prints: the block a launch of this
// stage would carry, and what it was built from. Block is "" when the record
// has nothing for the stage, which is an answer, not a failure. Prompt is
// filled only when the request asked for it — it carries the record rows and
// is the largest thing here by far.
type FeedbackReport struct {
	Key    string     `json:"key"`
	Stage  BoardStage `json:"stage"`
	Branch string     `json:"branch,omitempty"`
	Title  string     `json:"title,omitempty"`
	// Files is the stage's scope: the files whose rows were read first.
	Files []string `json:"files,omitempty"`
	// Rows is how many record rows the model was shown.
	Rows  int    `json:"rows"`
	Block string `json:"block"`
	// Cached is true when the block came from the launch cache rather than a
	// fresh model call — i.e. a launch (or an earlier ask) already built it
	// for this ticket, stage and record.
	Cached bool   `json:"cached"`
	Prompt string `json:"prompt,omitempty"`
}

// FeedbackExplainer answers one feedback request. Injected so the route stays
// a shaping step and the tracker resolution stays where every other route
// keeps it.
type FeedbackExplainer func(req FeedbackRequest) (FeedbackReport, error)

// feedbackStages are the stages the daemon launches with a briefing, so the
// only ones the question makes sense for. A stage outside the set is refused
// by name rather than answered from the record alone, which would look like
// a briefing no launch will ever carry.
var feedbackStages = []BoardStage{
	BoardPlanning, BoardTicketReview, BoardImplementation, BoardVerification,
	prReviewAgentStage, prFixAgentStage, deployFixAgentStage,
}

// FeedbackStageNames lists the stages `human feedback` accepts, for its help.
func FeedbackStageNames() []string {
	out := make([]string, 0, len(feedbackStages))
	for _, s := range feedbackStages {
		out = append(out, string(s))
	}
	return out
}

func isFeedbackStage(stage BoardStage) bool {
	for _, s := range feedbackStages {
		if s == stage {
			return true
		}
	}
	return false
}

// handleFeedback answers what a launch of one stage would be told. One JSON
// arg, mirroring fsm-where.
func (s *Server) handleFeedback(conn net.Conn, args []string) {
	if s.Feedback == nil {
		s.writeError(conn, "the launch briefing is disabled on this daemon: the review record could not be opened", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "feedback requires one JSON arg", 1)
		return
	}
	var req FeedbackRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid feedback request: "+err.Error(), 1)
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		s.writeError(conn, "feedback needs a ticket key", 1)
		return
	}
	if !isFeedbackStage(req.Stage) {
		s.writeError(conn, "no launch carries a briefing for stage \""+string(req.Stage)+"\"; one of: "+strings.Join(FeedbackStageNames(), ", "), 1)
		return
	}
	report, err := s.Feedback(req)
	if err != nil {
		s.writeError(conn, errors.CauseChain(err), 1)
		return
	}
	if !req.Raw {
		report.Prompt = ""
	}
	data, err := json.Marshal(report)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	_ = json.NewEncoder(conn).Encode(resp)
}
