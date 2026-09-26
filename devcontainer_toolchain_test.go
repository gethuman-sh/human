package main

import (
	"strings"
	"testing"

	"github.com/gethuman-sh/human/internal/devcontainer"
	"github.com/stretchr/testify/require"
)

// goFeatureKeyInRepo is the key .devcontainer/devcontainer.json uses for the Go
// stack; the wizard writes the same one (internal/init.goFeatureKey).
const goFeatureKeyInRepo = "ghcr.io/devcontainers/features/go:1"

// goDirectiveOf reads the version of go.mod's `go` directive. Parsed here
// independently of the production parser on purpose: this is the gate that
// catches the two files drifting, and a gate that shares its reading with the
// thing it judges can be wrong in both places at once.
func goDirectiveOf(t *testing.T, gomod string) string {
	t.Helper()
	for _, line := range strings.Split(gomod, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			return fields[1]
		}
	}
	t.Fatalf("go.mod declares no `go` directive")
	return ""
}

// TestDevcontainerPinsTheGoVersionGoModRequires: an unpinned go feature floats
// free of go.mod, so a go.mod bump silently outruns the image and every `go`
// invocation in an agent container then fails at toolchain selection — with the
// module proxy blocked, unrecoverably (SC-5879, after SC-4634 shipped nothing).
// CI expresses the same relationship with `go-version-file: go.mod`; the
// devcontainer feature has no such option, so the pin is checked here instead.
func TestDevcontainerPinsTheGoVersionGoModRequires(t *testing.T) {
	required := goDirectiveOf(t, readRepoFile(t, "go.mod"))

	cfg, err := devcontainer.ReadConfig(".")
	require.NoError(t, err, "reading .devcontainer/devcontainer.json")
	opts, ok := cfg.Features[goFeatureKeyInRepo].(map[string]any)
	require.Truef(t, ok, "%s must carry an options object holding its version pin", goFeatureKeyInRepo)
	pinned, _ := opts["version"].(string)

	require.Equalf(t, required, pinned,
		"go.mod requires go %s but the devcontainer pins the Go feature to %q — "+
			"the image would ship a Go the project refuses (SC-5879)", required, pinned)
}

// postStartCommandText flattens postStartCommand to the text a shell would
// see, whichever of the spec's shapes it takes — a bare string, or an object
// of named commands (internal/devcontainer/document.go). A test that only
// handles the string form breaks on a valid config the moment a named entry
// (e.g. step_lsp.go's "human-lsp") is added, for a shape reason unrelated to
// what it is gating (SC-5879 review).
func postStartCommandText(t *testing.T, raw any) string {
	t.Helper()
	switch v := raw.(type) {
	case string:
		return v
	case map[string]any:
		var parts []string
		for _, val := range v {
			s, ok := val.(string)
			require.Truef(t, ok, "named postStartCommand entry is not a string: %#v", val)
			parts = append(parts, s)
		}
		return strings.Join(parts, " && ")
	default:
		t.Fatalf("postStartCommand has an unhandled shape: %#v", raw)
		return ""
	}
}

// TestDevcontainerChecksTheToolchainAtContainerStart: the pin above is a
// declaration; this is what says so out loud in the container if the two ever
// disagree again, at start rather than deep inside a run.
func TestDevcontainerChecksTheToolchainAtContainerStart(t *testing.T) {
	cfg, err := devcontainer.ReadConfig(".")
	require.NoError(t, err)
	cmd := postStartCommandText(t, cfg.PostStartCommand)
	require.Contains(t, cmd, "human doctor toolchain",
		"the container bootstrap must check its Go against go.mod at start (SC-5879)")
}

// TestDevcontainerChecksTheToolchainBeforeLSPInstalls: the check must run
// after "human chrome-bridge" (so a mismatch never presents as a certificate
// failure at the model API, SC-4819) and BEFORE any `go install`/`npm
// install -g` LSP link — those links can themselves fail from the very Go
// version mismatch the check exists to report, and under the shell's `&&`
// chaining a failed earlier link short-circuits everything after it,
// including a check placed later (SC-5879 review: the check was unreachable
// under its own condition — proof in the review, .human/reviews/sc-5879.md).
func TestDevcontainerChecksTheToolchainBeforeLSPInstalls(t *testing.T) {
	cfg, err := devcontainer.ReadConfig(".")
	require.NoError(t, err)
	cmd := postStartCommandText(t, cfg.PostStartCommand)

	bridgeIdx := strings.Index(cmd, "human chrome-bridge")
	checkIdx := strings.Index(cmd, "human doctor toolchain")
	require.GreaterOrEqual(t, bridgeIdx, 0, "postStartCommand must run human chrome-bridge: %s", cmd)
	require.GreaterOrEqual(t, checkIdx, 0, "postStartCommand must run human doctor toolchain: %s", cmd)
	require.Less(t, bridgeIdx, checkIdx,
		"the toolchain check must run after chrome-bridge, got: %s", cmd)

	for _, lspCmd := range []string{"go install", "npm install -g"} {
		if lspIdx := strings.Index(cmd, lspCmd); lspIdx >= 0 {
			require.Lessf(t, checkIdx, lspIdx,
				"the toolchain check must run before %q so an install failure caused by "+
					"the toolchain mismatch cannot short-circuit the check under &&, got: %s", lspCmd, cmd)
		}
	}
}
