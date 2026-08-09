package claude

import (
	"bytes"
	_ "embed"
	"regexp"
	"slices"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

//go:embed embed/shared/exit-contract.md
var exitContractFragment []byte

//go:embed embed/shared/model-tiers.md
var modelTiersFragment []byte

// The rule (SC-2329): every board-launched stage carries the stage-lease
// include, so all nine agents the board can launch — planner, executor,
// reviewer, pr-reviewer, pr-fixer, deploy-fixer, bug-fixer, bug-triage,
// security-triage — hold a prompt-level lease as a uniform second line of
// defence behind the daemon's cross-daemon claim arbiter. A new board-launched
// stage inherits the rule rather than a precedent to guess at: add
// `<!-- human:include stage-lease stage=<its own stage> -->` to its prompt.
//
// The stage is a REQUIRED argument, and the fragment deliberately contains no
// usable example name. It used to carry `--stage fix` as a worked example and
// tell each agent to substitute its own; nine prompts copied the example
// instead. Because a lease is keyed (project, scope, stage), that put the
// planner, the executor, the PR reviewer and the fixers on ONE lease slot per
// ticket, where they blocked each other — the PR reviewer holding the lease the
// fixer needed is what surfaced as a bogus "decision needed" card. Naming the
// stage at the include site, and rejecting an include that omits it, is what
// makes the collision unrepresentable rather than merely discouraged.
//
//go:embed embed/shared/stage-lease.md
var stageLeaseFragment []byte

//go:embed embed/shared/build-gate.md
var buildGateFragment []byte

//go:embed embed/shared/outcome-not-mechanism.md
var outcomeNotMechanismFragment []byte

// The dependents taxonomy is shared because the failure it guards against is
// drift between the stage that lists a dependent and the stage that must act on
// it: the enumerating instruction existed in seven prompts, each phrased its own
// way, each scoped to Go call-graph symbols, while the dependencies that broke
// had no symbol at all. One copy of the kind->query table, the disposition
// vocabulary and the unchecked rule is what makes planner, verifier, implementer
// and reviewer talk about the same thing.
//
//go:embed embed/shared/dependents.md
var dependentsFragment []byte

// The machine an agent runs inside was written down and invisible to it: no
// prompt mentioned pipeline-fsm.json, so every agent decided what to post, and
// whether it was stuck, from its own prompt alone. This fragment is the half of
// `human fsm` that changes behaviour — the commands only make asking possible.
//
// The state is a REQUIRED argument for the reason stage-lease's is: the answer
// an agent needs is about the state it is actually in, and a fragment carrying
// an example state would be copied. It names the state an agent normally
// occupies rather than a stage, because stages are a coarser vocabulary that
// cannot answer "what may I post next" — and note it is NOT the lease scope,
// which is a third vocabulary again (`fix`, `pr-fix`, `triage`).
//
//go:embed embed/shared/fsm.md
var fsmFragment []byte

// sharedFragments are prompt blocks that must read identically in every skill
// and agent that carries them. Keeping one copy here and substituting it at
// install time is what stops twenty prompts from drifting apart, which is how
// the pipeline accumulated a different phrasing of the same rule per stage.
var sharedFragments = map[string][]byte{
	"exit-contract":         exitContractFragment,
	"model-tiers":           modelTiersFragment,
	"stage-lease":           stageLeaseFragment,
	"build-gate":            buildGateFragment,
	"outcome-not-mechanism": outcomeNotMechanismFragment,
	"dependents":            dependentsFragment,
	"fsm":                   fsmFragment,
}

// fragmentArgs names the arguments each shared fragment requires. A fragment
// absent from this map takes none, which keeps every existing include
// unchanged. An argument named here MUST be supplied at the include site and
// MUST appear in the fragment as its upper-cased placeholder (`stage` →
// `<STAGE>`), so a fragment can never ship a value a prompt was supposed to
// choose for itself.
var fragmentArgs = map[string][]string{
	"stage-lease": {"stage"},
	"fsm":         {"state"},
}

// includePattern matches a whole-line include directive, with optional
// space-separated key=value arguments:
//
//	<!-- human:include exit-contract -->
//	<!-- human:include stage-lease stage=pr-review -->
//
// It is an HTML comment so an un-expanded prompt still renders as valid
// markdown rather than showing markup to the model.
var includePattern = regexp.MustCompile(`(?m)^[ \t]*<!--[ \t]*human:include[ \t]+([a-z0-9-]+)((?:[ \t]+[a-z]+=[a-z0-9.-]+)*)[ \t]*-->[ \t]*$`)

// argPattern splits the argument tail of an include directive into key/value
// pairs.
var argPattern = regexp.MustCompile(`([a-z]+)=([a-z0-9.-]+)`)

// expandIncludes substitutes every shared fragment referenced by content.
//
// An unknown fragment name is an error rather than a silent pass-through: a
// prompt that ships with a dangling directive would quietly lose a rule the
// pipeline depends on, and that failure would only surface as an agent
// behaving oddly much later. A missing or unknown argument is an error for the
// same reason — an unbound placeholder reaching a model is a rule the agent
// then has to guess at.
func expandIncludes(content []byte) ([]byte, error) {
	var failure error
	expanded := includePattern.ReplaceAllFunc(content, func(match []byte) []byte {
		groups := includePattern.FindSubmatch(match)
		name := string(groups[1])
		fragment, ok := sharedFragments[name]
		if !ok {
			failure = errors.WithDetails("unknown shared prompt fragment", "fragment", name)
			return match
		}
		bound, err := bindFragmentArgs(name, fragment, string(groups[2]))
		if err != nil {
			failure = err
			return match
		}
		return bound
	})
	if failure != nil {
		return nil, failure
	}
	return expanded, nil
}

// bindFragmentArgs substitutes an include's key=value arguments into the
// fragment's placeholders. Every argument the fragment declares must be
// supplied, and nothing beyond them is accepted: a typo'd argument name is a
// silent no-op otherwise, which would leave the placeholder in the shipped
// prompt.
func bindFragmentArgs(name string, fragment []byte, tail string) ([]byte, error) {
	supplied := map[string]string{}
	for _, m := range argPattern.FindAllStringSubmatch(tail, -1) {
		supplied[m[1]] = m[2]
	}
	required := fragmentArgs[name]
	for key := range supplied {
		if !slices.Contains(required, key) {
			return nil, errors.WithDetails("unknown argument for shared prompt fragment", "fragment", name, "argument", key)
		}
	}
	bound := fragment
	for _, key := range required {
		value, ok := supplied[key]
		if !ok {
			return nil, errors.WithDetails("shared prompt fragment requires an argument", "fragment", name, "argument", key)
		}
		bound = bytes.ReplaceAll(bound, []byte("<"+strings.ToUpper(key)+">"), []byte(value))
	}
	return bound, nil
}
