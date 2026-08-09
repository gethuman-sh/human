package daemon

import "strings"

// OwnerOf answers "whose ticket is this?" from the two fields every tracker
// records: the assignee, falling back to the reporter when there is no assignee
// (SC-3339). Empty when neither is recorded — which is "owner unknown", NOT
// "owned by nobody in particular", and callers must decide which of those they
// are asking about.
//
// It lives here, in the package the daemon's work gate and the board's viewer
// overlay both import, because the two of them must agree: the board dims a card
// the daemon refuses to work, and a card the board shows as yours must be one the
// daemon will pick up. Two copies of this rule would drift into a board that lies
// about which cards the machine is actually driving.
func OwnerOf(assignee, reporter string) string {
	if a := strings.TrimSpace(assignee); a != "" {
		return a
	}
	return strings.TrimSpace(reporter)
}
