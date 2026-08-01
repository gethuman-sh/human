package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A shared rule reaches every member of its family only if each member actually
// carries the include and the fragment it names exists. This is the enforcement
// the SC-2404 audit asked for: a member that drops the rule — or a newly added
// member that never had it — fails here instead of shipping the old behaviour
// and waiting to be reported again from a different stage.
func TestRuleFamiliesShareTheirContract(t *testing.T) {
	require.NotEmpty(t, ruleFamilies, "the family registry must not be empty")
	for _, fam := range ruleFamilies {
		t.Run(fam.name, func(t *testing.T) {
			_, ok := sharedFragments[fam.include]
			require.Truef(t, ok,
				"family %q names include %q, which is not registered in sharedFragments", fam.name, fam.include)
			require.NotEmptyf(t, fam.members, "family %q declares no members", fam.name)

			directive := "human:include " + fam.include
			for _, member := range fam.members {
				body, err := os.ReadFile(filepath.Join("embed", member))
				require.NoErrorf(t, err, "reading family member %s", member)
				require.Containsf(t, string(body), directive,
					"%s is a member of family %q but does not carry `<!-- %s -->`; the shared rule would silently not reach it",
					member, fam.name, directive)
			}
		})
	}
}

// The outcome-over-mechanism rule now lives in one fragment carried by both
// review gates via the registry above. Its load-bearing clauses are asserted
// here on the single source (SC-2327), not re-checked per agent — the registry
// already proves both gates carry it, and expansion makes their copies
// identical by construction.
func TestOutcomeNotMechanismFragmentCarriesTheRule(t *testing.T) {
	content := string(sharedFragments["outcome-not-mechanism"])

	assert.Contains(t, content, "The outcome is the criterion, not the mechanism.",
		"the fragment must carry the outcome-over-mechanism rule")
	assert.Contains(t, content, "This holds for the **plan** as much as for the ticket",
		"the fragment must extend the rule to the plan, not only the ticket's own wording")
	assert.Contains(t, content, "different, equivalent, or cheaper route",
		"the fragment must state a cheaper/equivalent route is a PASS")
	assert.Contains(t, content, "ask about a skipped step only when its absence means something the ticket promised is missing",
		"the fragment must block a skipped step only when it leaves something promised missing")
	assert.Contains(t, content, "**Required approach**",
		"the fragment must keep an explicit Required approach binding")
	assert.NotContains(t, content, "For a bug ticket the acceptance criterion is the stated",
		"the fragment must not scope the rule to bug tickets alone")
}

// The live SC-2404 defect: the PR- and deploy-fixers named this repository's
// `make` targets as if universal, so on any non-Makefile project they told
// themselves to run commands that do not exist, while the bug-fixer detected
// the project's gate. Every fixer must now detect the gate (the shared rule) and
// none may name the full `make check` as the universal fast tier.
func TestFixerFamilyDetectsGateNotUniversalMake(t *testing.T) {
	var fixerFamily ruleFamily
	for _, fam := range ruleFamilies {
		if fam.name == "fixer-build-gate" {
			fixerFamily = fam
		}
	}
	require.NotEmpty(t, fixerFamily.members, "the fixer-build-gate family must exist")

	for _, member := range fixerFamily.members {
		body, err := os.ReadFile(filepath.Join("embed", member))
		require.NoErrorf(t, err, "reading %s", member)
		content := expanded(t, body)

		assert.Contains(t, content, "Detect the project's fast feedback gate; do not assume a fixed command.",
			"%s must carry the gate-detection rule after expansion", member)
		assert.NotContains(t, content, "not the full `make check`",
			"%s must not name this repo's `make check` as the universal fast tier", member)
	}
}
