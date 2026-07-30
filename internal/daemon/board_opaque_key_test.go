package daemon

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A ticket key is an opaque identifier: the daemon must carry whatever the
// tracker handed it and never assume a shape. Keys differ per backend (SC-1892,
// HUM-30, octocat/repo#42, Project/42) and a daemon that only survives one of
// those shapes loses the ticket's identity somewhere in the pipeline — which is
// exactly how a handover gets written under one spelling and read under another
// (SC-1892).
//
// The agent name is where that identity is most at risk, because it is the one
// place the key is encoded into another string and recovered from it. This locks
// the round trip for every key shape the tool supports, plus shapes it does not
// produce today, so the guarantee is about opacity rather than about the
// backends that happen to be wired up now.
func TestAgentNameRoundTripsAnyOpaqueKey(t *testing.T) {
	stages := []BoardStage{BoardImplementation, BoardVerification, prReviewAgentStage, prFixAgentStage, deployFixAgentStage}
	keys := []string{
		"SC-1892",          // Shortcut, the form it now emits
		"HUM-30",           // Linear / Jira
		"Stephan-Is-Great", // no digits, several hyphens
		"a",                // single character
		"1892",             // bare numeric, still readable from older state
	}
	for _, key := range keys {
		for _, stage := range stages {
			got, gotStage, ok := parseAgentName(agentNameFor(key, stage))
			require.True(t, ok, "agent name for %q/%q must parse back", key, stage)
			require.Equal(t, key, got, "key must survive the agent-name round trip")
			require.Equal(t, stage, gotStage, "stage must survive the agent-name round trip")
		}
	}
}

// Keys carrying characters the agent-name encoding replaces cannot round-trip
// verbatim, so they must still be MATCHED through the same sanitize() the
// launcher used — never by raw-key equality, which would silently fail to find
// a live agent for the ticket.
func TestOpaqueKeyWithEncodedCharactersStillMatchesItsAgent(t *testing.T) {
	// Anything outside [a-zA-Z0-9] is replaced, so these keys are lossy through
	// the encoding — including the underscore, which reads as name-safe but is not.
	for _, key := range []string{"octocat/repo#42", "Project/42", "UPPER_snake-42"} {
		name := agentNameFor(key, BoardImplementation)

		require.Equal(t, []string{name}, AgentsForPMKey([]string{name}, key),
			"a key whose characters the name encoding replaces must still match its own agent")

		parsed, _, ok := parseAgentName(name)
		require.True(t, ok)
		require.NotEqual(t, key, parsed,
			"guard the premise for %q: it is lossy through the encoding, which is why matching goes through sanitize()", key)
	}
}
