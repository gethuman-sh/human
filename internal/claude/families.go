package claude

// A ruleFamily is a set of agent prompts that must all obey the same
// cross-cutting rule. The rule lives in exactly one place — a shared include
// fragment registered in sharedFragments — and every member references it with
// the same `<!-- human:include <fragment> -->` directive, so one identical rule
// reaches the whole family at install time.
//
// This registry is the answer the SC-2404 audit asked for. Its recurring bug
// was a rule fixed on one agent and silently left to drift on its siblings
// (a build-gate rewrite that reached two agents and not their two every-card
// siblings; an outcome-over-mechanism rule written into one review gate and not
// its unattended pre-merge twin). The cause was share-by-copy: nothing said
// which agents formed a family, and nothing failed when one diverged. Here the
// families are declared once, discoverable without reading the git history, and
// TestRuleFamiliesShareTheirContract fails the build when a member — including a
// newly added one — is missing the family's include.
type ruleFamily struct {
	// name identifies the family in test failures and documentation.
	name string
	// include is the shared fragment every member must carry. It must be a key
	// of sharedFragments and is referenced as `<!-- human:include <include> -->`.
	include string
	// members are the embed/-relative prompt filenames bound by the rule.
	members []string
}

// ruleFamilies is the single source of record for which agents share which
// cross-cutting rule. Add a member here and it is enforced; forget to add the
// include to that member and the build goes red.
var ruleFamilies = []ruleFamily{
	{
		// The fixer agents all have one job — go green on the project's fast
		// feedback tier — and must detect that tier rather than assume this
		// repository's `make` targets. This is the live SC-2404 instance: the
		// PR- and deploy-fixers named `make` as universal while the bug-fixer
		// detected the gate, so on any non-Makefile project they were
		// unfollowable.
		name:    "fixer-build-gate",
		include: "build-gate",
		members: []string{
			"human-bug-fixer-agent.md",
			"human-pr-fixer-agent.md",
			"human-deploy-fixer-agent.md",
		},
	},
	{
		// Both review gates — the standalone reviewer and the unattended
		// pre-merge PR reviewer — must judge by the outcome, not the mechanism
		// the ticket or plan sketched. The rule was previously hand-copied into
		// each and guarded by a bespoke test (SC-2327); it is now one fragment
		// enforced by this registry like every other family.
		name:    "reviewer-outcome-not-mechanism",
		include: "outcome-not-mechanism",
		members: []string{
			"human-reviewer-agent.md",
			"human-pr-reviewer-agent.md",
		},
	},
	{
		// The two fix pipelines — bug and vulnerability — run the same shape of
		// work and dispatch the same stages, so which model tier each stage gets
		// has to read identically in both. They share that rule today only
		// because the security skill was made by copying the autofix one, which
		// is the share-by-copy this registry exists to end: nothing fails if one
		// drops the include, and a third fix pipeline would start without it.
		//
		// Registered as prevention rather than repair. The two have not drifted
		// (SC-2803) — the single change since the security skill was added
		// reached both — and no prose is folded together to hold them here.
		// Where the pipelines legitimately differ they keep their own words;
		// only the tier rule is one rule.
		name:    "fix-skill-model-tiers",
		include: "model-tiers",
		members: []string{
			"human-autofix-skill.md",
			"human-security-fix-skill.md",
		},
	},
}
