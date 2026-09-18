package cmddaemon

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The description editor renders the error's message as its whole status line,
// so a failed turn must carry its cause there — not only in the daemon log.
func TestTurnFailureMessage(t *testing.T) {
	msg := turnFailureMessage(claudeTurnOutput{
		IsError:        true,
		APIErrorStatus: 401,
		Result:         "Failed to authenticate. API Error: 401 OAuth access token has expired. Re-authenticate to continue.",
	})
	assert.Contains(t, msg, "OAuth access token has expired")
	assert.Contains(t, msg, "/login", "a refused login must name the host remedy")

	msg = turnFailureMessage(claudeTurnOutput{IsError: true, APIErrorStatus: 529, Result: "Overloaded"})
	assert.Equal(t, "agent turn failed: Overloaded", msg)
	assert.NotContains(t, msg, "/login")

	assert.Equal(t, "agent turn failed", turnFailureMessage(claudeTurnOutput{IsError: true}))
}

// The status line is one line and finite: a multi-line or runaway result is
// reduced to its first line and bounded.
func TestWithCause_isOneBoundedLine(t *testing.T) {
	assert.Equal(t, ": first", withCause("\n\n  first  \nsecond\n"))
	assert.Equal(t, "", withCause("   \n  "))

	long := withCause(strings.Repeat("x", 500))
	assert.True(t, strings.HasSuffix(long, "…"))
	assert.Less(t, len(long), 250)
}
