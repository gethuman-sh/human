package cmddaemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gethuman-sh/human/internal/tracker"
)

// A GitHub entry configured for pull requests is not a ticket source. Asked for
// a ticket listing it searches every issue the token can see, which is
// expensive, unrelated to this project's record, and rate limited — observed
// live tripping GitHub's secondary rate limit on every scheduled pass while
// contributing nothing (SC-2132).
func TestTicketSources_SkipsATrackerWithNoRole(t *testing.T) {
	got := ticketSources([]tracker.Instance{
		{Name: "human", Kind: "github"},   // credentials for the forge
		{Name: "human", Kind: "shortcut"}, // the PM tracker (role inferred)
	})

	names := make([]string, 0, len(got))
	for _, i := range got {
		names = append(names, i.Kind)
	}
	assert.Equal(t, []string{"shortcut"}, names,
		"only trackers carrying this project's work belong in the record")
}

// A team whose tracker IS GitHub declares role: pm, and must be indexed exactly
// as before — the rule is about role, not about which vendor it is.
func TestTicketSources_KeepsADeclaredTracker(t *testing.T) {
	got := ticketSources([]tracker.Instance{{Name: "work", Kind: "github", Role: "pm"}})

	assert.Len(t, got, 1, "a declared ticket tracker is a ticket source whatever its kind")
}

func TestTicketSources_EmptyInputIsEmpty(t *testing.T) {
	assert.Empty(t, ticketSources(nil))
}
