package cmdcapabilities

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/capabilities"
	"github.com/gethuman-sh/human/internal/env"
)

func run(t *testing.T, ctx context.Context, probe capabilities.RemoteProbe, args ...string) string {
	t.Helper()
	cmd := buildCapabilitiesCmd(probe)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	require.NoError(t, cmd.Execute())
	return out.String()
}

func hasRemote(context.Context) bool { return true }

func TestCapabilities_TextOutputForAStandaloneRun(t *testing.T) {
	out := run(t, context.Background(), hasRemote)

	require.Contains(t, out, "push       yes")
	require.Contains(t, out, "deploy     yes")
	require.Contains(t, out, "workspace  local")
}

// A board agent must be told plainly what it may not do, and why — the reason
// is what a stopping stage quotes back.
func TestCapabilities_BoardAgentIsToldWhyItCannotShip(t *testing.T) {
	ctx := env.WithEnv(context.Background(), map[string]string{
		"HUMAN_AGENT_NAME": "board-SC-9-implementation",
	})

	out := run(t, ctx, hasRemote)
	require.Contains(t, out, "push       no")
	require.Contains(t, out, "workspace  bind-mounted")
	require.Contains(t, out, "Deploy stage ships the work")
}

func TestCapabilities_JSONOutput(t *testing.T) {
	ctx := env.WithEnv(context.Background(), map[string]string{
		"HUMAN_AGENT_NAME": "board-SC-9-verification",
	})

	out := run(t, ctx, hasRemote, "--json")

	var set capabilities.Set
	require.NoError(t, json.Unmarshal([]byte(out), &set))
	require.True(t, set.BoardContext)
	require.False(t, set.CanOpenPR)
	require.Equal(t, "board-SC-9-verification", set.Agent)
}

func TestBuildCapabilitiesCmd_UsesTheRealProbe(t *testing.T) {
	cmd := BuildCapabilitiesCmd()
	require.Equal(t, "capabilities", cmd.Name())
	require.True(t, strings.Contains(cmd.Long, "runs locally"),
		"the help must say why it is not forwarded to the daemon")
}
