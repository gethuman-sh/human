package errors

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogError_EmitsCauseChainAndDetails(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Logger
	log.Logger = zerolog.New(&buf)
	defer func() { log.Logger = orig }()

	root := stderrors.New("dial unix /var/run/docker.sock: no such file or directory")
	err := WrapWithDetails(root, "starting agent container", "name", "fix-bug")

	LogError(err).Msg("command failed")

	out := buf.String()
	// The root cause must be surfaced, not just the outermost wrap message.
	assert.Contains(t, out, "starting agent container: dial unix /var/run/docker.sock")
	// Structured details still ride along.
	assert.Contains(t, out, "fix-bug")
}

func TestCauseChain(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		assert.Empty(t, CauseChain(nil))
	})

	t.Run("collapses duplicate wrap messages and surfaces root cause", func(t *testing.T) {
		root := stderrors.New("dial unix /var/run/docker.sock: no such file or directory")
		wrapped := WrapWithDetails(root, "starting agent container", "name", "fix-bug")

		// tozd reports the wrap message twice in the unwrap chain; the duplicate
		// is collapsed and the root cause is appended.
		assert.Equal(t,
			"starting agent container: dial unix /var/run/docker.sock: no such file or directory",
			CauseChain(wrapped))
	})

	t.Run("single error returns its message", func(t *testing.T) {
		assert.Equal(t, "boom", CauseChain(stderrors.New("boom")))
	})

	t.Run("does not duplicate a suffix already embedded by fmt.Errorf", func(t *testing.T) {
		root := stderrors.New("inner")
		// fmt.Errorf-style wrapping embeds the cause as a suffix.
		fmtWrapped := fmt.Errorf("outer: %w", root)
		assert.Equal(t, "outer: inner", CauseChain(fmtWrapped))
	})
}

func Test_isFormatVerb(t *testing.T) {
	tests := []struct {
		name string
		c    byte
		want bool
	}{
		{"d is a verb", 'd', true},
		{"s is a verb", 's', true},
		{"v is a verb", 'v', true},
		{"w is a verb", 'w', true},
		{"f is a verb", 'f', true},
		{"q is a verb", 'q', true},
		{"x is a verb", 'x', true},
		{"t is a verb", 't', true},
		{"percent is not a verb", '%', false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isFormatVerb(tt.c))
		})
	}
}

func Test_extractArgs(t *testing.T) {
	tests := []struct {
		name    string
		message string
		details []any
		want    []any
	}{
		{
			name:    "no placeholders no details",
			message: "simple message",
			details: nil,
			want:    nil,
		},
		{
			name:    "one placeholder one pair",
			message: "user %s failed",
			details: []any{"name", "alice"},
			want:    []any{"alice"},
		},
		{
			name:    "more args than placeholders truncates",
			message: "user %s failed",
			details: []any{"name", "alice", "code", 42},
			want:    []any{"alice"},
		},
		{
			name:    "fewer args than placeholders keeps all",
			message: "user %s code %d",
			details: []any{"name", "alice"},
			want:    []any{"alice"},
		},
		{
			name:    "multiple verbs",
			message: "%s returned %d with %v",
			details: []any{"op", "get", "status", 404, "body", "not found"},
			want:    []any{"get", 404, "not found"},
		},
		{
			name:    "percent-w is counted",
			message: "wrapping %w with %s",
			details: []any{"key", "val"},
			want:    []any{"val"},
		},
		{
			name:    "empty details",
			message: "msg %s",
			details: []any{},
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractArgs(tt.message, tt.details)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWithDetails(t *testing.T) {
	err := WithDetails("operation %s failed with code %d",
		"op", "create", "code", 500)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation create failed with code 500")

	details := AllDetails(err)
	assert.Equal(t, "create", details["op"])
	assert.Equal(t, 500, details["code"])
}

func TestWrapWithDetails(t *testing.T) {
	cause := WithDetails("root cause")
	err := WrapWithDetails(cause, "wrapping %s",
		"key", "val")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrapping val")

	details := AllDetails(err)
	assert.Equal(t, "val", details["key"])
}
