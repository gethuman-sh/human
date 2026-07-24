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

// CreateVariationsRequest launches the human-mockups skill in variation mode:
// spawn a new group of variations of one existing mockup (ParentSlug/ParentFile)
// honoring free-text Instructions. PMKey ties the new group to the same ticket
// tree; Feature carries the parent's feature name so the agent keeps context
// without a tracker round-trip.
type CreateVariationsRequest struct {
	PMKey        string `json:"pm_key"`
	Feature      string `json:"feature"`
	ParentSlug   string `json:"parent_slug"`
	ParentFile   string `json:"parent_file"`
	Instructions string `json:"instructions"`
}

// ChooseMockupRequest marks a leaf mockup as the ticket's winner (or clears it
// when Slug is empty).
type ChooseMockupRequest struct {
	PMKey string `json:"pm_key"`
	Slug  string `json:"slug"`
	File  string `json:"file"`
}

// PruneMockupRequest archives a variation group and its descendants. The root
// group of a ticket cannot be pruned (it is the entry point).
type PruneMockupRequest struct {
	PMKey string `json:"pm_key"`
	Slug  string `json:"slug"`
}
