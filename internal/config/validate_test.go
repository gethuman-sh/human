package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rules returns the rule ids a config trips, which is what a test should assert
// on: the wording is meant to change as we learn to explain something better.
func rules(problems []Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.Rule)
	}
	return out
}

func TestValidate_healthyConfigIsQuiet(t *testing.T) {
	doc := parse(t, `
shortcuts:
  - name: board
    role: pm
    token: sc
forges:
  - name: prs
    token: gh
`)
	assert.Empty(t, doc.Validate())
}

// The rule that used to live inside a provider's loader, where nothing else
// could consult it.
func TestValidate_retiredForgeRole(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: prs\n    token: t\n    role: forge\nforges:\n  - name: other\n    token: t\n")

	problems := doc.Validate()
	assert.Contains(t, rules(problems), "retired-forge-role")
	require.True(t, Errors(problems), "an entry that cannot load is an error, not advice")
}

// The cross-section rule — the one no layer could express before, because none
// of them held two sections at once.
func TestValidate_halfMigratedGitHub(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: human\n    token: t\nforges:\n  - name: human\n    token: t\n")

	problems := doc.Validate()
	assert.Contains(t, rules(problems), "half-migrated-github")
	assert.True(t, Errors(problems))

	for _, p := range problems {
		if p.Rule == "half-migrated-github" {
			assert.Contains(t, p.Message, "rate-limited search",
				"the rule must say what it COSTS, not merely that it is untidy")
			assert.Equal(t, "human config migrate", p.Fix)
		}
	}
}

// A declared tracker sharing a forge name is the deliberate case and must stay
// quiet: GitHub issues and GitHub pull requests under one name is a real setup.
func TestValidate_declaredTrackerBesideAForgeIsFine(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: human\n    role: pm\n    token: t\n    projects:\n      - acme/web\nforges:\n  - name: human\n    token: t\n")

	assert.NotContains(t, rules(doc.Validate()), "half-migrated-github")
}

// The fault a loader can never catch: the entry is correct, the role is right,
// and the behaviour is still wrong ([SC-3888]).
func TestValidate_unscopedGitHubTracker(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: work\n    role: pm\n    token: t\nforges:\n  - name: prs\n    token: t\n")

	problems := doc.Validate()
	assert.Contains(t, rules(problems), "unscoped-github-tracker")
	assert.False(t, Errors(problems), "it works — it is just expensive, so it warns")

	for _, p := range problems {
		if p.Rule == "unscoped-github-tracker" {
			assert.Contains(t, p.Message, "every issue the token can see")
			assert.Contains(t, p.Fix, "projects:")
		}
	}
}

func TestValidate_scopedGitHubTrackerIsQuiet(t *testing.T) {
	doc := parse(t, "githubs:\n  - name: work\n    role: pm\n    token: t\n    projects:\n      - acme/web\nforges:\n  - name: prs\n    token: t\n")

	assert.NotContains(t, rules(doc.Validate()), "unscoped-github-tracker")
}

// A tracker with no projects on any other backend is the documented "show all
// work", and cheap. The rule is about GitHub's endpoint, not about scoping.
func TestValidate_unscopedNonGitHubTrackerIsQuiet(t *testing.T) {
	doc := parse(t, "shortcuts:\n  - name: board\n    role: pm\n    token: t\nforges:\n  - name: prs\n    token: t\n")

	assert.NotContains(t, rules(doc.Validate()), "unscoped-github-tracker")
}

func TestValidate_duplicateNamesWithinASection(t *testing.T) {
	doc := parse(t, "jiras:\n  - name: work\n    token: a\n  - name: work\n    token: b\n")

	problems := doc.Validate()
	assert.Contains(t, rules(problems), "duplicate-name")
	assert.True(t, Errors(problems))
}

// A tracker and a forge may share a name: different domains, different lists,
// resolved separately. Reporting that would punish the config we recommend.
func TestValidate_trackerAndForgeMaySharAName(t *testing.T) {
	doc := parse(t, "shortcuts:\n  - name: human\n    role: pm\n    token: t\nforges:\n  - name: human\n    token: t\n")

	assert.NotContains(t, rules(doc.Validate()), "duplicate-name")
}

func TestValidate_noForgeWarns(t *testing.T) {
	doc := parse(t, "shortcuts:\n  - name: board\n    role: pm\n    token: t\n")

	problems := doc.Validate()
	assert.Contains(t, rules(problems), "no-forge")
	assert.False(t, Errors(problems), "plenty of setups never open a pull request")
}

// The state this project's own config was in this morning, caught by the object
// rather than by someone reading command output twice.
func TestValidate_theLiveHalfMigratedConfig(t *testing.T) {
	doc := parse(t, `
githubs:
  - name: human
    token: gh://token
shortcuts:
  - name: human
    role: pm
    token: 1pw://Private/Shortcut Token/notesPlain
forges:
  - name: human
    token: gh://token
`)

	problems := doc.Validate()
	assert.Contains(t, rules(problems), "half-migrated-github")
	assert.Contains(t, rules(problems), "unscoped-github-tracker")
	assert.True(t, Errors(problems))
}

func TestProblem_StringCarriesTheFix(t *testing.T) {
	p := Problem{Severity: Warning, Message: "something costs more than you think", Fix: "do this"}
	assert.Equal(t, "warning: something costs more than you think — do this", p.String())
}
