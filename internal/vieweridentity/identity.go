// Package vieweridentity resolves who is looking at the board, so a card owned
// by someone else can be told apart from one of your own. The identity is
// declared in the .humanconfig "me" section rather than asked of a tracker: an
// API answer is one provider's opinion, needs a live call and a credential to
// obtain, and silently withholds the answer when it fails — at which point
// every ticket looks like yours.
//
// Names, not emails, because names are the only key every backend puts on a
// card: GitHub and GitLab carry a login/username on an assignee and expose no
// email at all, and Jira hides emailAddress behind privacy settings. It is a
// LIST for the same reason — the same person is "Stephan Schmidt" on Shortcut
// and a login on GitHub, so one entry per identity you hold.
package vieweridentity

import (
	"strings"

	"github.com/gethuman-sh/human/internal/config"
)

// Identity is the set of names that mean "me" across the configured trackers.
type Identity struct {
	Names []string `mapstructure:"names"`
}

// Load reads the "me" section from .humanconfig in dir. A missing file or
// section yields an empty identity, which every consumer reads as "viewer
// unknown" and leaves the board undimmed. Package var so callers can stub it.
var Load = func(dir string) (Identity, error) {
	var id Identity
	if err := config.UnmarshalSection(dir, "me", &id); err != nil {
		return Identity{}, err
	}
	return id.cleaned(), nil
}

// cleaned drops blank entries and surrounding whitespace so a stray list item
// or a trailing space in the config cannot make a name un-matchable.
func (i Identity) cleaned() Identity {
	out := make([]string, 0, len(i.Names))
	for _, n := range i.Names {
		if trimmed := strings.TrimSpace(n); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return Identity{}
	}
	return Identity{Names: out}
}

// Matches reports whether name is one of the viewer's identities. Comparison is
// case-insensitive: a tracker that title-cases a login must not turn a card of
// yours into someone else's. An empty name never matches — "no owner recorded"
// is not "owned by me".
func (i Identity) Matches(name string) bool {
	candidate := strings.TrimSpace(name)
	if candidate == "" {
		return false
	}
	for _, n := range i.Names {
		if strings.EqualFold(n, candidate) {
			return true
		}
	}
	return false
}

// Known reports whether the viewer declared any identity at all. Callers use it
// to distinguish "not mine" from "no idea who you are", which must render
// differently: the first dims, the second must not.
func (i Identity) Known() bool { return len(i.Names) > 0 }
