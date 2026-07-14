package daemon

// CreateMocksRequest is the wire request for launching the human-mockups
// skill for one PM ticket from the board's card context menu. Title and
// description travel with the key so the generating agent gets the feature
// context without another tracker round-trip.
type CreateMocksRequest struct {
	PMKey       string `json:"pm_key"`
	PMTitle     string `json:"pm_title"`
	Description string `json:"description,omitempty"`
}
