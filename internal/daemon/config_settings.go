package daemon

import (
	"encoding/json"
	"net"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/settings"
)

// SetConfigRequest is the config-set wire payload. Value stays raw JSON so
// one route serves every field type; the settings schema decides how to
// decode it.
type SetConfigRequest struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// handleConfigGet returns the masked settings snapshot for the request's
// project directory. Reads never resolve vault references — 1pw:// values
// travel verbatim and literal secrets are masked — so no credential can
// leave the daemon through this route.
func (s *Server) handleConfigGet(conn net.Conn, projectDir string) {
	doc, err := settings.Snapshot(projectDir)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeConfigDoc(conn, doc)
}

// handleConfigSet writes one settings key via the comment-preserving YAML
// writer and returns the fresh snapshot, so the client refreshes in a single
// round trip. Like board-transition this is not destructive-gated: the
// settings form itself is the user's consent.
func (s *Server) handleConfigSet(conn net.Conn, args []string, projectDir string) {
	if len(args) != 1 {
		s.writeError(conn, "config-set requires one JSON arg", 1)
		return
	}
	var req SetConfigRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid config-set request: "+err.Error(), 1)
		return
	}
	var value any
	if err := json.Unmarshal(req.Value, &value); err != nil {
		s.writeError(conn, "invalid config-set value: "+err.Error(), 1)
		return
	}
	if err := settings.SetValue(projectDir, req.Path, value); err != nil {
		// Validation detail (masked sentinel, enum violation, …) lives in the
		// wrapped cause; surface the full chain so the UI can show it inline.
		s.writeError(conn, errors.CauseChain(err), 1)
		return
	}
	doc, err := settings.Snapshot(projectDir)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeConfigDoc(conn, doc)
}

func (s *Server) writeConfigDoc(conn net.Conn, doc settings.Doc) {
	data, err := json.Marshal(doc)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}
