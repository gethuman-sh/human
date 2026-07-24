package daemon

import (
	"time"
)

// IdeationMaxAge bounds how long a saved-but-unfinished ideation session stays
// restorable. A chat abandoned days ago should not reappear on the board after
// a restart, but one interrupted minutes ago by a daemon self-restart should.
const IdeationMaxAge = 24 * time.Hour

// PersistedIdeation is the serializable form of the engine's single ideation
// session. The session's own fields are unexported (engine-internal, guarded by
// its mutex), so this is the explicit wire/storage contract between the engine
// and a store.
//
// ResumeID is the load-bearing field: the agent conversation itself lives with
// the provider, so restoring the session id, state and resume id is enough to
// carry a chat across a restart.
type PersistedIdeation struct {
	ID              string            `json:"id"`
	Mode            IdeationMode      `json:"mode,omitempty"`
	State           IdeationState     `json:"state"`
	Transcript      []IdeationMessage `json:"transcript,omitempty"`
	ResumeID        string            `json:"resume_id,omitempty"`
	Question        *IdeationQuestion `json:"question,omitempty"`
	Draft           *IdeationDraft    `json:"draft,omitempty"`
	CreatedKey      string            `json:"created_key,omitempty"`
	CreatedURL      string            `json:"created_url,omitempty"`
	ErrMsg          string            `json:"err_msg,omitempty"`
	RepairAttempted bool              `json:"repair_attempted,omitempty"`
	EvolveKey       string            `json:"evolve_key,omitempty"`
	EvolveLabels    []string          `json:"evolve_labels,omitempty"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// IdeationStore persists the single ideation session so it survives a daemon
// restart — including the self-restart handover, which can land between two
// turns of a live chat. Implementations must be safe for concurrent use.
type IdeationStore interface {
	// Save replaces the stored session with p.
	Save(p PersistedIdeation) error
	// Load returns the stored session, or (nil, nil) when none is stored.
	Load() (*PersistedIdeation, error)
	// Clear removes the stored session.
	Clear() error
}

// persist snapshots the session for storage. Caller must hold the engine mutex.
func (s *ideationSession) persist() PersistedIdeation {
	return PersistedIdeation{
		ID:              s.id,
		Mode:            s.mode,
		State:           s.state,
		Transcript:      s.transcript,
		ResumeID:        s.resumeID,
		Question:        s.question,
		Draft:           s.draft,
		CreatedKey:      s.createdKey,
		CreatedURL:      s.createdURL,
		ErrMsg:          s.errMsg,
		RepairAttempted: s.repairAttempted,
		EvolveKey:       s.evolveKey,
		EvolveLabels:    s.evolveLabels,
		UpdatedAt:       time.Now().UTC(),
	}
}

// restoreSession rebuilds an in-memory session from storage.
func restoreSession(p PersistedIdeation) *ideationSession {
	return &ideationSession{
		id:              p.ID,
		mode:            p.Mode,
		state:           p.State,
		transcript:      p.Transcript,
		resumeID:        p.ResumeID,
		question:        p.Question,
		draft:           p.Draft,
		createdKey:      p.CreatedKey,
		createdURL:      p.CreatedURL,
		errMsg:          p.ErrMsg,
		repairAttempted: p.RepairAttempted,
		evolveKey:       p.EvolveKey,
		evolveLabels:    p.EvolveLabels,
	}
}

// isTerminal reports whether a session has finished and has nothing left to
// resume — its ticket was created, or it failed.
func (p PersistedIdeation) isTerminal() bool {
	return p.State == IdeationDone || p.State == IdeationError || p.State == IdeationNone
}

// restorable reports whether a stored session should be brought back at
// startup: still mid-conversation, and recent enough to still be wanted.
func (p PersistedIdeation) restorable(now time.Time, maxAge time.Duration) bool {
	if p.ID == "" || p.isTerminal() {
		return false
	}
	return now.Sub(p.UpdatedAt) <= maxAge
}

// normalizeForRestore repairs a state that cannot survive a process boundary.
// A session saved as "thinking" had its agent turn running in the old process;
// that goroutine died with it, so nothing will ever complete the turn. It comes
// back as an error the user can retry from, never a spinner that hangs forever.
func normalizeForRestore(p PersistedIdeation) PersistedIdeation {
	if p.State == IdeationThinking {
		p.State = IdeationError
		p.ErrMsg = "the daemon restarted while this turn was running — start or reply again to continue"
	}
	return p
}
