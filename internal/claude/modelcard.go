package claude

import (
	_ "embed"
	"encoding/json"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// ModelCardPath is where the card lives, for messages that have to name it —
// a rate that looks wrong should send a reader to one file, not to a search.
const ModelCardPath = "internal/claude/models.json"

//go:embed models.json
var rawModelCard []byte

// ModelFamily is one priced family. Rates are USD per 1e6 tokens of each class;
// the four classes are priced very differently, so they are never blended.
type ModelFamily struct {
	Family          string  `json:"family"`
	Display         string  `json:"display"`
	InputPerM       float64 `json:"input_per_m"`
	OutputPerM      float64 `json:"output_per_m"`
	CacheCreatePerM float64 `json:"cache_create_per_m"`
	CacheReadPerM   float64 `json:"cache_read_per_m"`
	// ValidUntil is the last day (UTC, "2006-01-02") the rate is known to hold.
	// It is DATA rather than a comment because a rate that outlives its window
	// is wrong silently, and silence is what this file exists to end: a test
	// fails the build the day after it lapses. Empty means no announced expiry.
	ValidUntil string `json:"valid_until,omitempty"`
	Provenance string `json:"provenance"`
}

// IgnoredEntry is something that appears in the model field of a transcript but
// is not a model. It carries its reason so the next reader does not have to
// rediscover why a row is missing from a panel.
type IgnoredEntry struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// ModelCard is the parsed card. Lookup maps are built once at parse time
// because PriceFor is called per JSONL line on the daemon's hot path.
type ModelCard struct {
	Doc            string         `json:"doc"`
	FallbackFamily string         `json:"fallback_family"`
	Families       []ModelFamily  `json:"families"`
	TaskAliases    []string       `json:"task_aliases"`
	Ignored        []IgnoredEntry `json:"ignored"`

	byFamily map[string]ModelFamily
	ignored  map[string]IgnoredEntry
	fallback ModelFamily
}

// modelCard is the card, compiled in and parsed once.
//
// A malformed card panics here rather than degrading to a default, because a
// rate card that silently falls back is the exact failure this file replaces:
// wrong money with no signal. The file is compiled in, so it can only be broken
// by an edit, and TestModelCard_parses fails that edit before it ships.
var modelCard = mustModelCard()

func mustModelCard() *ModelCard {
	card, err := parseModelCard(rawModelCard)
	if err != nil {
		panic("human: " + ModelCardPath + " is not a usable rate card: " + err.Error())
	}
	return card
}

// parseModelCard decodes and validates the card. Validation is here rather than
// in a test alone so a caller can never hold a half-usable card.
func parseModelCard(raw []byte) (*ModelCard, error) {
	var card ModelCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, errors.WrapWithDetails(err, "decode model card", "path", ModelCardPath)
	}
	if len(card.Families) == 0 {
		return nil, errors.WithDetails("model card has no families", "path", ModelCardPath)
	}
	if err := card.indexFamilies(); err != nil {
		return nil, err
	}
	if err := card.resolveFallback(); err != nil {
		return nil, err
	}
	if err := card.checkTaskAliases(); err != nil {
		return nil, err
	}
	if err := card.indexIgnored(); err != nil {
		return nil, err
	}
	return &card, nil
}

// indexFamilies builds the lookup map, rejecting a family the card cannot price
// with. The key is trimmed and lowercased because it is matched against a family
// word derived from a transcript, which carries whatever casing the vendor used.
func (c *ModelCard) indexFamilies() error {
	c.byFamily = make(map[string]ModelFamily, len(c.Families))
	for _, f := range c.Families {
		name := strings.ToLower(strings.TrimSpace(f.Family))
		if name == "" {
			return errors.WithDetails("model card family has no name", "path", ModelCardPath)
		}
		if _, dup := c.byFamily[name]; dup {
			return errors.WithDetails("model card family declared twice", "family", name)
		}
		if err := f.validate(name); err != nil {
			return err
		}
		c.byFamily[name] = f
	}
	return nil
}

// validate rejects a row that cannot be used as money. A zero rate is the
// dangerous one: it reads as free rather than as missing.
func (f ModelFamily) validate(name string) error {
	if f.Display == "" {
		return errors.WithDetails("model card family has no display name", "family", name)
	}
	if f.Provenance == "" {
		return errors.WithDetails("model card family has no provenance", "family", name)
	}
	if f.InputPerM <= 0 || f.OutputPerM <= 0 || f.CacheCreatePerM <= 0 || f.CacheReadPerM <= 0 {
		return errors.WithDetails("model card family has a non-positive rate", "family", name)
	}
	if _, _, err := f.validUntil(); err != nil {
		return err
	}
	return nil
}

func (c *ModelCard) resolveFallback() error {
	f, ok := c.byFamily[strings.ToLower(strings.TrimSpace(c.FallbackFamily))]
	if !ok {
		return errors.WithDetails("model card fallback_family names no declared family",
			"fallback_family", c.FallbackFamily)
	}
	c.fallback = f
	return nil
}

// checkTaskAliases enforces the one-directional invariant: every dispatchable
// model must be priced, while a priced family need not be dispatchable. The
// other direction would make adding a rate silently claim the model can be
// passed to Task.
func (c *ModelCard) checkTaskAliases() error {
	if len(c.TaskAliases) == 0 {
		return errors.WithDetails("model card declares no task aliases", "path", ModelCardPath)
	}
	for _, alias := range c.TaskAliases {
		if _, ok := c.byFamily[strings.ToLower(strings.TrimSpace(alias))]; !ok {
			return errors.WithDetails("task alias has no priced family — a dispatchable model that cannot be priced bills at the ceiling",
				"alias", alias)
		}
	}
	return nil
}

func (c *ModelCard) indexIgnored() error {
	c.ignored = make(map[string]IgnoredEntry, len(c.Ignored))
	for _, ig := range c.Ignored {
		id := strings.ToLower(strings.TrimSpace(ig.ID))
		if id == "" {
			return errors.WithDetails("model card ignored entry has no id", "path", ModelCardPath)
		}
		if ig.Reason == "" {
			return errors.WithDetails("model card ignored entry has no reason", "id", id)
		}
		c.ignored[id] = ig
	}
	return nil
}

// validUntil reports the end of the rate's validity as an instant. The stored
// date is the last day the rate holds, so the instant is the END of that day in
// UTC — a rate valid "until 2026-08-31" is not stale at 00:01 on the 31st.
// ok is false when the family announces no expiry.
func (f ModelFamily) validUntil() (t time.Time, ok bool, err error) {
	if strings.TrimSpace(f.ValidUntil) == "" {
		return time.Time{}, false, nil
	}
	d, perr := time.ParseInLocation("2006-01-02", strings.TrimSpace(f.ValidUntil), time.UTC)
	if perr != nil {
		return time.Time{}, false, errors.WrapWithDetails(perr, "parse valid_until",
			"family", f.Family, "valid_until", f.ValidUntil)
	}
	return d.AddDate(0, 0, 1), true, nil
}

// prices projects a family onto the rate card shape callers price with.
func (f ModelFamily) prices() TokenPrices {
	return TokenPrices{
		InputPerM:       f.InputPerM,
		OutputPerM:      f.OutputPerM,
		CacheCreatePerM: f.CacheCreatePerM,
		CacheReadPerM:   f.CacheReadPerM,
	}
}

// familyOf reduces any shape of model identity to the card's lookup key.
//
// This is the whole point of the card owning normalisation: the cost ledger
// stores the vendor's raw id ("claude-sonnet-4-5-20250929") while the transcript
// scan stores a classified display name ("sonnet 4.5"). Keying on one shape
// prices the other at the fallback forever, which is how per-ticket cost stayed
// billed at opus while the stats panel looked fixed (SC-3580). classifyModel
// already understands every id shape — the Bedrock prefix, the legacy
// version-first form, date stamps — so identity is derived once, not twice.
func familyOf(model string) string {
	family := classifyModel(model)
	if i := strings.IndexByte(family, ' '); i >= 0 {
		family = family[:i] // "sonnet 4.5" -> "sonnet"
	}
	return family
}

// familyFor returns the card row for a model, and whether the card knew it.
func familyFor(model string) (ModelFamily, bool) {
	f, ok := modelCard.byFamily[familyOf(model)]
	if !ok {
		return modelCard.fallback, false
	}
	return f, true
}

// isIgnoredModel reports whether a transcript's model field names something
// that is not a model. Callers must drop the whole entry rather than hide the
// row later: a non-model filtered at render time keeps contributing to the
// headline while disappearing from the panel.
func isIgnoredModel(model string) bool {
	_, ok := modelCard.ignored[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

// taskModelAliases returns the Task tool's model alias vocabulary. It lives in
// the card because "what a model is called" is model identity; keeping it here
// stops the set from being restated in a prompt and a test that then drift.
func taskModelAliases() []string {
	out := make([]string, len(modelCard.TaskAliases))
	copy(out, modelCard.TaskAliases)
	return out
}
