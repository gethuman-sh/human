// Package appearance resolves how the board looks for the person in front of
// it, from the .humanconfig "ui" section. It is the sibling of
// internal/vieweridentity: ownership dimming has two inputs — who counts as me,
// and how faint everyone else renders — and both belong in the same file rather
// than split across two stores. .humanconfig is per-machine (both it and
// .humanconfig.yaml are gitignored), so a preference set here never travels
// with a checkout.
//
// The strength is an INTEGER PERCENT, not the CSS opacity it becomes: the
// settings schema (internal/settings) has no float field type, so a percent is
// the only shape the existing generic settings form can render and save.
package appearance

import (
	"github.com/gethuman-sh/human/internal/config"
)

const (
	// DefaultDimPercent is the shipped value, kept in lockstep with the
	// --not-mine-opacity fallback in desktop/frontend/static/style.css. A style
	// guard test asserts the two agree.
	DefaultDimPercent = 35
	// MinDimPercent keeps a not-mine card readable: below this the card is
	// effectively invisible, which is a board that lost a ticket rather than
	// one that de-emphasised it.
	MinDimPercent = 5
	// MaxDimPercent is full opacity — an explicit "do not dim at all", which a
	// solo user legitimately wants and which is different from declaring
	// nothing.
	MaxDimPercent = 100
)

// Appearance is the declared board appearance. One field today; the section
// exists so the next appearance knob is one field and one token.
type Appearance struct {
	// Dim is how visible a card owned by someone else renders, in percent of
	// full opacity. Zero means undeclared — see DimPercent.
	Dim int `mapstructure:"dim_percent"`
}

// Load reads the "ui" section from .humanconfig in dir. A missing file or
// section yields the zero Appearance, which reads as "declared nothing" and
// leaves the shipped stylesheet default in force. Package var so callers can
// stub it, matching vieweridentity.Load.
//
// The target is deliberately a FRESH zero struct: a bare "ui:" decodes as nil
// input, which mapstructure passes over without writing, so a pre-seeded
// default would survive and masquerade as a declared value.
var Load = func(dir string) (Appearance, error) {
	var a Appearance
	if err := config.UnmarshalSection(dir, "ui", &a); err != nil {
		return Appearance{}, err
	}
	return a, nil
}

// DimPercent is the declared dimming strength, or 0 when the project declared
// nothing usable. An out-of-range value is REJECTED rather than clamped to a
// bound: clamping would invent an intent nobody expressed, while 0 lets every
// consumer fall back to the one shipped default. Zero is therefore the single
// "say nothing" answer for a missing file, a missing section, an empty value, a
// value cleared from the settings page, and a nonsense one alike.
func (a Appearance) DimPercent() int {
	if a.Dim < MinDimPercent || a.Dim > MaxDimPercent {
		return 0
	}
	return a.Dim
}

// Declared reports whether the project set a usable dimming strength at all.
// Callers use it to leave the stylesheet untouched rather than re-assert the
// default, so an unconfigured project renders through exactly today's path.
func (a Appearance) Declared() bool { return a.DimPercent() != 0 }
