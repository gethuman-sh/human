package daemon

import (
	"encoding/json"
	"net"
)

func (s *Server) writeDescEditStatus(conn net.Conn, st DescEditStatus) {
	data, err := json.Marshal(st)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	resp := Response{Stdout: string(data) + "\n"}
	enc := json.NewEncoder(conn)
	_ = enc.Encode(resp)
}

func (s *Server) handleDescEditStart(conn net.Conn, args []string) {
	if s.DescEdit == nil {
		s.writeError(conn, "description edit not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "descedit-start requires one JSON arg", 1)
		return
	}
	var req DescEditStartRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid descedit-start request: "+err.Error(), 1)
		return
	}
	st, err := s.DescEdit.Start(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeDescEditStatus(conn, st)
}

func (s *Server) handleDescEditReply(conn net.Conn, args []string) {
	if s.DescEdit == nil {
		s.writeError(conn, "description edit not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "descedit-reply requires one JSON arg", 1)
		return
	}
	var req DescEditReplyRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid descedit-reply request: "+err.Error(), 1)
		return
	}
	st, err := s.DescEdit.Reply(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeDescEditStatus(conn, st)
}

func (s *Server) handleDescEditApply(conn net.Conn, args []string) {
	if s.DescEdit == nil {
		s.writeError(conn, "description edit not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "descedit-apply requires one JSON arg", 1)
		return
	}
	var req DescEditApplyRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid descedit-apply request: "+err.Error(), 1)
		return
	}
	st, err := s.DescEdit.Apply(req)
	if err != nil {
		s.writeError(conn, err.Error(), 1)
		return
	}
	s.writeDescEditStatus(conn, st)
}

func (s *Server) handleDescEditStatus(conn net.Conn) {
	if s.DescEdit == nil {
		s.writeError(conn, "description edit not available", 1)
		return
	}
	s.writeDescEditStatus(conn, s.DescEdit.Status())
}

func (s *Server) handleDescEditDiscard(conn net.Conn, args []string) {
	if s.DescEdit == nil {
		s.writeError(conn, "description edit not available", 1)
		return
	}
	if len(args) != 1 {
		s.writeError(conn, "descedit-discard requires one JSON arg", 1)
		return
	}
	var req DescEditDiscardRequest
	if err := json.Unmarshal([]byte(args[0]), &req); err != nil {
		s.writeError(conn, "invalid descedit-discard request: "+err.Error(), 1)
		return
	}
	s.writeDescEditStatus(conn, s.DescEdit.Discard(req))
}
