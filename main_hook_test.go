package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

// A newer Claude Code renamed the event key; the daemon is unreachable.
// The forwarder must (a) still recover and forward the event and
// (b) surface the delivery failure on stderr — never exit silently.
func TestRunHook_RenamedEventKey_UnreachableDaemon(t *testing.T) {
	payload := `{"eventName":"Stop","session_id":"s1","cwd":"/w","tool_name":""}`

	var captured []string
	deliver := func(args []string) error {
		captured = args
		return errors.WithDetails("cannot reach daemon", "addr", "127.0.0.1:1")
	}

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err, "hook must never fail the calling process")
	require.NotEmpty(t, captured, "event must be forwarded even under a renamed key")
	require.Equal(t, "hook-event", captured[0])
	assert.Equal(t, "Stop", captured[1], "event name must be recovered from an aliased key")
	assert.NotEmpty(t, stderr.String(), "a failed delivery must leave a visible diagnostic")
	assert.Contains(t, stderr.String(), "Stop")
}

// A body with no recognizable event key must warn on stderr, not vanish.
func TestRunHook_UnknownEventKey_WarnsAndDropsWithoutDelivery(t *testing.T) {
	payload := `{"totally_unknown":"x"}`

	delivered := false
	deliver := func([]string) error { delivered = true; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err)
	assert.False(t, delivered, "no event name means nothing to deliver")
	assert.NotEmpty(t, stderr.String(), "an unrecognized non-empty body must warn on stderr")
}

// Canonical current-schema payload still works and forwards all fields.
func TestRunHook_CanonicalKey_ForwardsSuccessfully(t *testing.T) {
	payload := `{"hook_event_name":"PostToolUse","session_id":"s2","cwd":"/w","tool_name":"Bash"}`

	var captured []string
	deliver := func(args []string) error { captured = args; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader(payload), &stderr, deliver)

	require.NoError(t, err)
	require.Len(t, captured, 8)
	assert.Equal(t, "PostToolUse", captured[1])
	assert.Equal(t, "s2", captured[2])
	assert.Equal(t, "/w", captured[3])
	assert.Equal(t, "Bash", captured[5])
	assert.Empty(t, stderr.String(), "a successful delivery must stay quiet")
}

// An empty stdin invocation is a genuine no-op — no warning noise.
func TestRunHook_EmptyBody_NoWarnNoDeliver(t *testing.T) {
	delivered := false
	deliver := func([]string) error { delivered = true; return nil }

	var stderr bytes.Buffer
	err := runHook(strings.NewReader("   \n"), &stderr, deliver)

	require.NoError(t, err)
	assert.False(t, delivered)
	assert.Empty(t, stderr.String())
}
