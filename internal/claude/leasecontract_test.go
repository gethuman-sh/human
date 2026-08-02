package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A stage lease is keyed (project, scope, stage), so the stage name IS the
// identity of a pipeline step. When two steps name the same stage they share
// one lock per ticket: they block each other while alive, and inherit each
// other's leftovers when one dies. That is not a hypothetical — the shared
// fragment used to carry `--stage fix` as a worked example and 58 leases were
// taken under `fix` by three different roles, including the PR reviewer holding
// the lease the PR fixer needed.
//
// This test is the lease-side twin of stagecontract_test.go, which already
// enforces the same discipline for the `stage.<name>` records. The records half
// had a test and stayed correct; the lease half had none and drifted.

var (
	leaseIncludePattern = regexp.MustCompile(`<!--\s*human:include\s+stage-lease\b([^>]*)-->`)
	leaseStagePattern   = regexp.MustCompile(`\bstage=([a-z0-9.-]+)`)
)

// leaseStages maps every prompt carrying the stage-lease include to the stage
// it declares, and fails a prompt that carries the include without one.
func leaseStages(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir("embed")
	require.NoError(t, err)

	stages := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("embed", e.Name()))
		require.NoError(t, err)
		m := leaseIncludePattern.FindSubmatch(body)
		if m == nil {
			continue
		}
		arg := leaseStagePattern.FindSubmatch(m[1])
		require.NotNil(t, arg,
			"%s includes stage-lease without stage=<its own stage>; a lease with no owner name is how nine prompts ended up sharing one lock",
			e.Name())
		stages[e.Name()] = string(arg[1])
	}
	require.NotEmpty(t, stages, "no prompt carries the stage-lease include — the locator is wrong, not the prompts")
	return stages
}

// Every board-launched stage must lease under a name that identifies it. Two
// prompts may share a stage only when they are the same step of two pipelines
// that never run on one ticket (autofix and security-fix triage) — the same
// exemption stagecontract_test.go grants the record writers.
func TestLeaseStagesIdentifyTheirStep(t *testing.T) {
	sharedAcrossPipelines := map[string]bool{"triage": true}

	byStage := map[string][]string{}
	for prompt, stage := range leaseStages(t) {
		byStage[stage] = append(byStage[stage], prompt)
	}
	for stage, prompts := range byStage {
		if len(prompts) == 1 || sharedAcrossPipelines[stage] {
			continue
		}
		require.Failf(t, "stage lease collision",
			"prompts %v all lease stage %q, so they hold ONE lock per ticket and will block each other; give each its own stage",
			prompts, stage)
	}
}

// The lease stage and the stage a prompt records its handoff under must agree.
// They are the same step by two names, and a disagreement means a successor
// inherits state filed somewhere it will not look.
func TestLeaseStageMatchesRecordedStage(t *testing.T) {
	recordPattern := regexp.MustCompile(`human state set (?:<[A-Z_]+>|SC-\d+) stage\.([a-z-]+)\b`)

	for prompt, stage := range leaseStages(t) {
		body := readEmbed(t, prompt)
		m := recordPattern.FindStringSubmatch(body)
		if m == nil {
			// A stage that records no handoff still needs a lease; there is
			// simply nothing to cross-check it against.
			continue
		}
		require.Equal(t, m[1], stage,
			"%s leases stage %q but records its handoff under stage.%s — a successor would inherit nothing",
			prompt, stage, m[1])
	}
}

// The fragment must not ship a stage name of its own. A usable literal in the
// shared text is exactly what nine prompts copied, and re-adding one would
// reintroduce the collision without failing any other test.
func TestStageLeaseFragmentCarriesNoLiteralStage(t *testing.T) {
	body := string(stageLeaseFragment)
	require.Contains(t, body, "--stage <STAGE>",
		"the fragment's lease command must use the <STAGE> placeholder, never a real stage name")
	require.NotContains(t, body, "--stage fix",
		"the fragment must not carry a copyable stage name — that literal is the original defect")
}

// An include that omits a required argument must fail the install rather than
// ship a prompt with an unbound placeholder in it.
func TestExpandIncludesRequiresFragmentArgs(t *testing.T) {
	_, err := expandIncludes([]byte("<!-- human:include stage-lease -->\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires an argument")

	_, err = expandIncludes([]byte("<!-- human:include stage-lease stg=review -->\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown argument")

	out, err := expandIncludes([]byte("<!-- human:include stage-lease stage=pr-review -->\n"))
	require.NoError(t, err)
	require.Contains(t, string(out), "--stage pr-review")
	require.NotContains(t, string(out), "<STAGE>", "every placeholder must be bound before the prompt ships")
}
