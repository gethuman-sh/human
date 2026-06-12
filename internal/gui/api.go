package gui

import (
	"encoding/json"
	"net/http"
	"strings"
)

// writeJSON encodes v with a JSON content type. Encoding failures after
// the header is sent cannot be reported to the client; they are logged.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.Logger.Warn().Err(err).Msg("gui response encode failed")
	}
}

// apiError is the uniform error envelope for all endpoints.
type apiError struct {
	Error string `json:"error"`
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.Snapshots == nil {
		s.writeError(w, http.StatusServiceUnavailable, "snapshots not available")
		return
	}
	dto, err := s.Snapshots.Snapshot(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleProjects(w http.ResponseWriter, _ *http.Request) {
	if s.Projects == nil {
		s.writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.writeJSON(w, http.StatusOK, s.Projects())
}

func (s *Server) handleIssues(w http.ResponseWriter, _ *http.Request) {
	if s.Issues == nil {
		s.writeJSON(w, http.StatusOK, []any{})
		return
	}
	results, err := s.Issues()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, results)
}

// logModeBody is the PUT /api/log-mode request and the response of both
// log-mode endpoints.
type logModeBody struct {
	Mode string `json:"mode"`
}

func (s *Server) handleLogModeGet(w http.ResponseWriter, _ *http.Request) {
	if s.LogMode == nil {
		s.writeError(w, http.StatusServiceUnavailable, "log mode not available")
		return
	}
	s.writeJSON(w, http.StatusOK, logModeBody{Mode: s.LogMode.Get()})
}

func (s *Server) handleLogModeSet(w http.ResponseWriter, r *http.Request) {
	if s.LogMode == nil {
		s.writeError(w, http.StatusServiceUnavailable, "log mode not available")
		return
	}
	var body logModeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.LogMode.Set(body.Mode); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, logModeBody{Mode: s.LogMode.Get()})
}

func (s *Server) handleConfirmsGet(w http.ResponseWriter, _ *http.Request) {
	if s.Confirms == nil {
		s.writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.writeJSON(w, http.StatusOK, s.Confirms.Snapshot())
}

// confirmBody is the POST /api/confirms/{id} request.
type confirmBody struct {
	Approved bool `json:"approved"`
}

func (s *Server) handleConfirmResolve(w http.ResponseWriter, r *http.Request) {
	if s.Confirms == nil {
		s.writeError(w, http.StatusServiceUnavailable, "confirmations not available")
		return
	}
	var body confirmBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	id := r.PathValue("id")
	if err := s.Confirms.Resolve(id, body.Approved, s.ApproverPID); err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"approved": body.Approved})
}

// ticketBody is the POST /api/tickets request.
type ticketBody struct {
	TrackerKind string `json:"tracker_kind"`
	Project     string `json:"project"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// ticketCreated is the POST /api/tickets response.
type ticketCreated struct {
	Key         string `json:"key"`
	TrackerKind string `json:"tracker_kind"`
}

func (s *Server) handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	if s.Commands == nil {
		s.writeError(w, http.StatusServiceUnavailable, "ticket creation not available")
		return
	}
	var body ticketBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.TrackerKind == "" || body.Title == "" {
		s.writeError(w, http.StatusBadRequest, "tracker_kind and title are required")
		return
	}

	// Same arg shape as the TUI's createTicketCmd so both surfaces hit the
	// identical CLI code path (incl. destructive-op interception).
	args := []string{body.TrackerKind, "issue", "create", "--project=" + body.Project, body.Title}
	if body.Description != "" {
		args = append(args, "--description", body.Description)
	}
	out, err := s.Commands.RunCapture(args)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	key := strings.TrimSpace(string(out))
	if i := strings.IndexByte(key, '\t'); i >= 0 {
		key = key[:i]
	}
	s.writeJSON(w, http.StatusCreated, ticketCreated{Key: key, TrackerKind: body.TrackerKind})
}

// agentBody is the POST /api/agents request.
type agentBody struct {
	Prompt     string `json:"prompt"`
	ProjectDir string `json:"project_dir,omitempty"`
}

// agentDispatched is the POST /api/agents response.
type agentDispatched struct {
	Name string `json:"name"`
}

func (s *Server) handleAgentDispatch(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		s.writeError(w, http.StatusServiceUnavailable, "agent dispatch not available")
		return
	}
	var body agentBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Prompt == "" {
		s.writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	name, err := s.Agents.Dispatch(r.Context(), DispatchOpts(body))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 202: the container is still starting; the instance appears in the
	// snapshot stream once discovery picks it up.
	s.writeJSON(w, http.StatusAccepted, agentDispatched{Name: name})
}

func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	if s.Agents == nil {
		s.writeError(w, http.StatusServiceUnavailable, "agent dispatch not available")
		return
	}
	name := r.PathValue("name")
	if err := s.Agents.Stop(r.Context(), name); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, agentDispatched{Name: name})
}
