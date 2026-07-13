package daemon

// CloseTicketRequest is the wire request for closing a PM ticket from the board
// (transitioning it to its Done status). It is a dedicated route — not the
// `issue status` CLI path — so it bypasses the interactive pending-confirm gate;
// the drag-and-confirm in the desktop UI is the user's consent.
type CloseTicketRequest struct {
	PMKey string `json:"pm_key"`
}
