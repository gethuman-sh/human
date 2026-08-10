package claude

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validValuesPattern extracts the alias vocabulary from the shared tier
// fragment's one sentence that states it.
var validValuesPattern = regexp.MustCompile("Valid values are exactly ([^—]+)—")
var backtickedPattern = regexp.MustCompile("`([a-z]+)`")

func TestModelCard_parses(t *testing.T) {
	card, err := parseModelCard(rawModelCard)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(card.Families), 4)
	assert.Equal(t, "fable", card.fallback.Family)
	assert.NotEmpty(t, card.TaskAliases)
	assert.NotEmpty(t, card.Doc, "the card must say what it is for — it is the file a rate correction lands in")
}

// A card that decodes but does not hold together must fail loudly at parse
// time, because the alternative is a rate card that silently degrades to a
// default: wrong money, no signal, which is the bug SC-3580 closes.
func TestModelCard_rejectsMalformedCards(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "not json",
			raw:  `{`,
			want: "decode model card",
		},
		{
			name: "no families",
			raw:  `{"fallback_family":"opus","families":[],"task_aliases":["opus"]}`,
			want: "no families",
		},
		{
			name: "unknown fallback",
			raw:  `{"fallback_family":"nope","families":[` + validFamilyJSON("opus") + `],"task_aliases":["opus"]}`,
			want: "fallback_family names no declared family",
		},
		{
			name: "duplicate family",
			raw: `{"fallback_family":"opus","families":[` +
				validFamilyJSON("opus") + `,` + validFamilyJSON("opus") + `],"task_aliases":["opus"]}`,
			want: "declared twice",
		},
		{
			name: "no name",
			raw:  `{"fallback_family":"opus","families":[{"family":"","display":"X","input_per_m":1,"output_per_m":1,"cache_create_per_m":1,"cache_read_per_m":1,"provenance":"p"}],"task_aliases":["opus"]}`,
			want: "family has no name",
		},
		{
			name: "no display",
			raw:  `{"fallback_family":"opus","families":[{"family":"opus","input_per_m":1,"output_per_m":1,"cache_create_per_m":1,"cache_read_per_m":1,"provenance":"p"}],"task_aliases":["opus"]}`,
			want: "no display name",
		},
		{
			name: "no provenance",
			raw:  `{"fallback_family":"opus","families":[{"family":"opus","display":"Opus","input_per_m":1,"output_per_m":1,"cache_create_per_m":1,"cache_read_per_m":1}],"task_aliases":["opus"]}`,
			want: "no provenance",
		},
		{
			name: "non-positive rate",
			raw:  `{"fallback_family":"opus","families":[{"family":"opus","display":"Opus","input_per_m":1,"output_per_m":0,"cache_create_per_m":1,"cache_read_per_m":1,"provenance":"p"}],"task_aliases":["opus"]}`,
			want: "non-positive rate",
		},
		{
			name: "unparseable valid_until",
			raw:  `{"fallback_family":"opus","families":[{"family":"opus","display":"Opus","input_per_m":1,"output_per_m":1,"cache_create_per_m":1,"cache_read_per_m":1,"valid_until":"31-08-2026","provenance":"p"}],"task_aliases":["opus"]}`,
			want: "parse valid_until",
		},
		{
			name: "no task aliases",
			raw:  `{"fallback_family":"opus","families":[` + validFamilyJSON("opus") + `],"task_aliases":[]}`,
			want: "no task aliases",
		},
		{
			name: "unpriced task alias",
			raw:  `{"fallback_family":"opus","families":[` + validFamilyJSON("opus") + `],"task_aliases":["opus","mythos"]}`,
			want: "task alias has no priced family",
		},
		{
			name: "ignored entry without id",
			raw:  `{"fallback_family":"opus","families":[` + validFamilyJSON("opus") + `],"task_aliases":["opus"],"ignored":[{"id":"","reason":"r"}]}`,
			want: "ignored entry has no id",
		},
		{
			name: "ignored entry without reason",
			raw:  `{"fallback_family":"opus","families":[` + validFamilyJSON("opus") + `],"task_aliases":["opus"],"ignored":[{"id":"<synthetic>"}]}`,
			want: "ignored entry has no reason",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseModelCard([]byte(tt.raw))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func validFamilyJSON(family string) string {
	return `{"family":"` + family + `","display":"D","input_per_m":1,"output_per_m":1,` +
		`"cache_create_per_m":1,"cache_read_per_m":1,"provenance":"p"}`
}

// A rate with no provenance is a number nobody can check. The opus row's
// measurement is the one the ticket names by criterion, so it is pinned
// literally: it is what justifies a card that was derived rather than published.
func TestModelCard_everyFamilyCarriesProvenance(t *testing.T) {
	for _, f := range modelCard.Families {
		assert.NotEmptyf(t, f.Provenance, "the %s rate has no provenance", f.Family)
	}
	opus := modelCard.byFamily["opus"]
	assert.Contains(t, opus.Provenance, "105231")
	assert.Contains(t, opus.Provenance, "SC-2549")
}

// An unrecognised model must read as expensive, never as free — so the fallback
// row has to be the most expensive one on every class. This is mechanical
// because the invariant otherwise rots the moment a family lands above the
// current ceiling.
func TestModelCard_fallbackIsTheMostExpensiveRow(t *testing.T) {
	fb := modelCard.fallback
	for _, f := range modelCard.Families {
		assert.LessOrEqualf(t, f.InputPerM, fb.InputPerM,
			"%s input costs more than the fallback %s: set fallback_family to %q in %s", f.Family, fb.Family, f.Family, ModelCardPath)
		assert.LessOrEqualf(t, f.OutputPerM, fb.OutputPerM,
			"%s output costs more than the fallback %s: set fallback_family to %q in %s", f.Family, fb.Family, f.Family, ModelCardPath)
		assert.LessOrEqualf(t, f.CacheCreatePerM, fb.CacheCreatePerM,
			"%s cache-write costs more than the fallback %s: set fallback_family to %q in %s", f.Family, fb.Family, f.Family, ModelCardPath)
		assert.LessOrEqualf(t, f.CacheReadPerM, fb.CacheReadPerM,
			"%s cache-read costs more than the fallback %s: set fallback_family to %q in %s", f.Family, fb.Family, f.Family, ModelCardPath)
	}
}

// A rate that outlives its validity window is wrong silently, which is the class
// of bug SC-3580 closes. The window is data in the card, and this is the check
// that turns it into a build failure the day after it lapses.
func TestModelCard_noRateHasOutlivedItsWindow(t *testing.T) {
	now := time.Now().UTC()
	for _, f := range modelCard.Families {
		end, ok, err := f.validUntil()
		require.NoError(t, err)
		if !ok {
			continue
		}
		require.Falsef(t, now.After(end),
			"the %s rate was only valid until %s and that window has lapsed: re-verify the four rates for %q in %s, then set a new valid_until or remove it (provenance: %s)",
			f.Display, f.ValidUntil, f.Family, ModelCardPath, f.Provenance)
	}
}

// The stored date is the LAST day the rate holds, so a rate valid "until the
// 31st" is still valid at 23:59 on the 31st and stale on the 1st. Getting this
// off by a day would either expire a correct rate or keep a lapsed one.
func TestModelFamily_validUntilIsTheEndOfTheStoredDay(t *testing.T) {
	f := ModelFamily{Family: "sonnet", ValidUntil: "2026-08-31"}
	end, ok, err := f.validUntil()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), end)

	_, ok, err = ModelFamily{Family: "opus"}.validUntil()
	require.NoError(t, err)
	assert.False(t, ok, "an absent valid_until means no announced expiry, not an expiry at the zero time")
}

// The shared tier fragment states the alias vocabulary in prose, and prose is a
// home for a closed set exactly as much as a switch is. Bind the sentence to the
// card so the two cannot drift; if the sentence is reworded, this test says so.
func TestModelCard_taskAliasesMatchTheTierFragment(t *testing.T) {
	body, err := os.ReadFile("embed/shared/model-tiers.md")
	require.NoError(t, err)

	m := validValuesPattern.FindStringSubmatch(string(body))
	require.NotNil(t, m, "embed/shared/model-tiers.md no longer states 'Valid values are exactly …' — the alias vocabulary must stay stated where an agent reads it")

	var fromProse []string
	for _, b := range backtickedPattern.FindAllStringSubmatch(m[1], -1) {
		fromProse = append(fromProse, b[1])
	}
	assert.ElementsMatch(t, taskModelAliases(), fromProse,
		"%s and embed/shared/model-tiers.md disagree about the Task alias vocabulary", ModelCardPath)
}

// The tier fragment names aliases in two places: the "Valid values are exactly"
// sentence and the Task(…) examples above it. The repo-wide dispatch test walks
// embed/*.md only, so an alias inside embed/shared/ is checked by nothing —
// model-tiers.md:6 already carries model="sonnet" that neither test reads.
func TestModelCard_tierFragmentTaskExamplesUseKnownAliases(t *testing.T) {
	body, err := os.ReadFile("embed/shared/model-tiers.md")
	require.NoError(t, err)

	aliases := taskModelAliases()
	found := 0
	// taskModelPattern is the existing package-level regexp declared in
	// stagecontract_test.go — reuse it rather than declaring a second one, so
	// this check and the repo-wide dispatch test agree by construction on what
	// counts as a dispatch. Its model group is the SECOND submatch.
	for _, m := range taskModelPattern.FindAllStringSubmatch(string(body), -1) {
		found++
		assert.Containsf(t, aliases, m[2],
			"embed/shared/model-tiers.md dispatches model=%q, which %s does not price", m[2], ModelCardPath)
	}
	require.Positivef(t, found, "embed/shared/model-tiers.md no longer contains a Task(… model=…) example — if the examples moved, move this check with them")
}

// The tier table below the sentence names the same families. Matching is trim
// plus lowercase so a transcript's whitespace or case variant is still caught.
func TestModelCard_ignoredEntriesAreRecognised(t *testing.T) {
	assert.True(t, isIgnoredModel("<synthetic>"))
	assert.True(t, isIgnoredModel(" <SYNTHETIC> "))
	assert.False(t, isIgnoredModel("claude-opus-4-8"))
	assert.False(t, isIgnoredModel(""))
}

// Every ignored entry says why it is ignored, so a missing panel row can be
// explained from the card rather than from git history.
func TestModelCard_ignoredEntriesCarryTheirReason(t *testing.T) {
	require.NotEmpty(t, modelCard.Ignored)
	for _, ig := range modelCard.Ignored {
		assert.NotEmptyf(t, ig.Reason, "ignored entry %q has no reason", ig.ID)
		assert.Containsf(t, strings.ToLower(ig.Reason), "sc-3580", "ignored entry %q should cite the ticket that excluded it", ig.ID)
	}
}

// familyFor tells a caller whether the card KNEW the model, which is what
// separates "priced at the sonnet row" from "priced at the ceiling because we
// have never heard of it".
func TestFamilyFor_reportsWhetherTheCardKnewTheModel(t *testing.T) {
	f, known := familyFor("claude-sonnet-4-5-20250929")
	assert.True(t, known)
	assert.Equal(t, "sonnet", f.Family)

	f, known = familyFor("gpt-4o")
	assert.False(t, known)
	assert.Equal(t, modelCard.fallback.Family, f.Family)
}
