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

// TestDevcontainerChecksTheToolchainAtContainerStart: the pin above is a
// declaration; this is what says so out loud in the container if the two ever
// disagree again, at start rather than deep inside a run.
func TestDevcontainerChecksTheToolchainAtContainerStart(t *testing.T) {
	cfg, err := devcontainer.ReadConfig(".")
	require.NoError(t, err)
	cmd, ok := cfg.PostStartCommand.(string)
	require.True(t, ok, "postStartCommand must be the string form this repo uses")
	require.Contains(t, cmd, "human doctor toolchain",
		"the container bootstrap must check its Go against go.mod at start (SC-5879)")
}
