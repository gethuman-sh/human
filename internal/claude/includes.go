package claude

import (
	_ "embed"
	"regexp"

	"github.com/gethuman-sh/human/errors"
)

//go:embed embed/shared/exit-contract.md
var exitContractFragment []byte

// sharedFragments are prompt blocks that must read identically in every skill
// and agent that carries them. Keeping one copy here and substituting it at
// install time is what stops twenty prompts from drifting apart, which is how
// the pipeline accumulated a different phrasing of the same rule per stage.
var sharedFragments = map[string][]byte{
	"exit-contract": exitContractFragment,
}

// includePattern matches a whole-line include directive:
//
//	<!-- human:include exit-contract -->
//
// It is an HTML comment so an un-expanded prompt still renders as valid
// markdown rather than showing markup to the model.
var includePattern = regexp.MustCompile(`(?m)^[ \t]*<!--[ \t]*human:include[ \t]+([a-z0-9-]+)[ \t]*-->[ \t]*$`)

// expandIncludes substitutes every shared fragment referenced by content.
//
// An unknown fragment name is an error rather than a silent pass-through: a
// prompt that ships with a dangling directive would quietly lose a rule the
// pipeline depends on, and that failure would only surface as an agent
// behaving oddly much later.
func expandIncludes(content []byte) ([]byte, error) {
	var unknown string
	expanded := includePattern.ReplaceAllFunc(content, func(match []byte) []byte {
		name := string(includePattern.FindSubmatch(match)[1])
		fragment, ok := sharedFragments[name]
		if !ok {
			unknown = name
			return match
		}
		return fragment
	})
	if unknown != "" {
		return nil, errors.WithDetails("unknown shared prompt fragment", "fragment", unknown)
	}
	return expanded, nil
}
