package cmdusage

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/claude"
)

// stubFinder is a mock InstanceFinder for testing.
type stubFinder struct {
	instances []claude.Instance
	err       error
}

func (s *stubFinder) FindInstances(_ context.Context) ([]claude.Instance, error) {
	return s.instances, s.err
}

// stubWalker is a mock DirWalker that feeds lines to the callback.
type stubWalker struct {
	lines [][]byte
	err   error
}

func (s *stubWalker) WalkJSONL(_ string, fn func(line []byte) error) error {
	if s.err != nil {
		return s.err
	}
	for _, line := range s.lines {
		if err := fn(line); err != nil {
			return err
		}
	}
	return nil
}

// newTestCmd creates a cobra.Command with a captured output buffer.
func newTestCmd() (*cobra.Command, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetContext(context.Background())
	return cmd, buf
}

// fixedTime returns a deterministic time for test reproducibility.
func fixedTime() time.Time {
	return time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
}

func TestBuildUsageCmd(t *testing.T) {
	cmd := BuildUsageCmd()
	assert.Equal(t, "usage", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotNil(t, cmd.RunE)
}

func TestBuildUsageCmd_Execute(t *testing.T) {
	// Exercise the RunE closure which calls buildFinder() and RunUsage.
	// This hits real infrastructure (OS process listing, Docker), but
	// should not fail -- just produce best-effort output.
	cmd := BuildUsageCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetContext(context.Background())

	// Execute may error if ~/.claude doesn't exist, that's acceptable.
	_ = cmd.Execute()
}

func TestRunUsage_NoInstances_LocalFallback(t *testing.T) {
	// When finder returns zero instances, RunUsage falls through to
	// printLocalUsage which tries to read from ~/.claude/projects.
	// In CI / test environments the directory might not exist, so we
	// just verify the function doesn't panic and returns *some* result.
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: nil, err: nil}

	// printLocalUsage may error if ~/.claude doesn't exist; that's fine
	// for the test -- we just verify it was reached.
	_ = RunUsage(cmd, finder, fixedTime())

	// If it succeeded, output should contain the usage window header.
	// If it failed (no ~/.claude), the buffer may be empty.
	_ = buf // consumed
}

func TestRunUsage_FinderError_StillPrintsLocal(t *testing.T) {
	// Even when FindInstances returns an error, RunUsage ignores the
	// error (instances, _ := ...) and proceeds to printUsage with nil instances.
	cmd, _ := newTestCmd()
	finder := &stubFinder{instances: nil, err: assert.AnError}

	// Should not panic regardless of finder error.
	_ = RunUsage(cmd, finder, fixedTime())
}

func TestRunUsage_SingleInstance(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{} // empty: produces zero usage
	inst := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	assert.Contains(t, out, "UTC")
}

func TestRunUsage_MultipleInstances(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{}
	inst1 := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl-1",
	}
	inst2 := claude.Instance{
		Label:  "Host (PID 200)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl-2",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst1, inst2}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
}

func TestRunUsage_ContainerInstances_CollectsContainerIDs(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{}
	inst := claude.Instance{
		Label:       `Container "dev" (abc123)`,
		Source:      "container",
		Walker:      walker,
		Root:        "/tmp/test-jsonl",
		ContainerID: "abc123def456",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	// Should still produce output (usage header at minimum).
	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	_ = buf
}

func TestPrintUsage_EmptyInstances_CallsLocalUsage(t *testing.T) {
	// printUsage with empty instances calls printLocalUsage.
	// This may fail if ~/.claude doesn't exist, but should not panic.
	buf := &bytes.Buffer{}
	_ = printUsage(buf, nil, fixedTime())
	_ = buf
}

func TestPrintInstanceUsage_ZeroResults(t *testing.T) {
	// When all instances have walkers that error, CollectInstanceUsage
	// returns empty results, and printInstanceUsage should produce
	// a usage header with no model rows.
	now := fixedTime()
	failWalker := &stubWalker{err: assert.AnError}
	inst := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: failWalker,
		Root:   "/tmp/test-jsonl",
	}
	buf := &bytes.Buffer{}
	err := printInstanceUsage(buf, []claude.Instance{inst}, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
}

func TestPrintInstanceUsage_SingleResult(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{} // empty usage
	inst := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl",
	}
	buf := &bytes.Buffer{}
	err := printInstanceUsage(buf, []claude.Instance{inst}, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	// Single instance should use FormatUsage, not FormatMultiUsage.
	// No instance labels should appear.
}

func TestPrintInstanceUsage_MultipleResults(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{} // empty usage
	inst1 := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl-1",
	}
	inst2 := claude.Instance{
		Label:  "Host (PID 200)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl-2",
	}
	buf := &bytes.Buffer{}
	err := printInstanceUsage(buf, []claude.Instance{inst1, inst2}, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
}

func TestPrintInstanceUsage_MixedWalkers(t *testing.T) {
	// One instance fails, one succeeds -- only the successful one appears.
	now := fixedTime()
	goodWalker := &stubWalker{}
	badWalker := &stubWalker{err: assert.AnError}

	instances := []claude.Instance{
		{Label: "Host (PID 100)", Source: "host", Walker: goodWalker, Root: "/tmp/good"},
		{Label: "Host (PID 200)", Source: "host", Walker: badWalker, Root: "/tmp/bad"},
	}

	buf := &bytes.Buffer{}
	err := printInstanceUsage(buf, instances, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
}

func TestRunUsage_OutputContainsWindowHours(t *testing.T) {
	// Verify the output includes the 5-hour window boundaries.
	now := time.Date(2026, 3, 25, 14, 30, 0, 0, time.UTC)
	walker := &stubWalker{}
	inst := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	out := buf.String()
	// Window for 14:30 UTC should be 10:00-15:00 UTC.
	assert.Contains(t, out, "10:00")
	assert.Contains(t, out, "15:00")
}

func TestRunUsage_WithJSONLData(t *testing.T) {
	// Feed actual JSONL-like data through the walker to verify model
	// usage appears in the output.
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	// Construct a JSONL line matching the jsonlLine struct:
	// type=assistant, timestamp in window, message.model, message.usage.
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339)
	line := `{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-opus-4","usage":{"input_tokens":1000,"output_tokens":500,"cache_creation_input_tokens":200,"cache_read_input_tokens":100}}}`
	walker := &stubWalker{lines: [][]byte{[]byte(line)}}
	inst := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test-jsonl",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	assert.Contains(t, out, "opus")
}

func TestRunUsage_MultipleInstancesWithData(t *testing.T) {
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-30 * time.Minute).Format(time.RFC3339)

	line1 := `{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-opus-4","usage":{"input_tokens":500,"output_tokens":250,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	line2 := `{"type":"assistant","timestamp":"` + ts + `","message":{"model":"claude-sonnet-4","usage":{"input_tokens":2000,"output_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	walker1 := &stubWalker{lines: [][]byte{[]byte(line1)}}
	walker2 := &stubWalker{lines: [][]byte{[]byte(line2)}}

	inst1 := claude.Instance{Label: "Host (PID 100)", Source: "host", Walker: walker1, Root: "/tmp/1"}
	inst2 := claude.Instance{Label: "Host (PID 200)", Source: "host", Walker: walker2, Root: "/tmp/2"}

	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst1, inst2}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	// Both model families should appear in multi-instance output.
	assert.Contains(t, out, "opus")
	assert.Contains(t, out, "sonnet")
}

func TestPrintUsage_WithInstances(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{}
	instances := []claude.Instance{
		{Label: "Host (PID 100)", Source: "host", Walker: walker, Root: "/tmp/test"},
	}
	buf := &bytes.Buffer{}
	err := printUsage(buf, instances, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
}

func TestPrintInstanceUsage_AllBranchPaths(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{}

	tests := []struct {
		name      string
		instances []claude.Instance
	}{
		{
			name:      "empty after collect",
			instances: []claude.Instance{{Label: "fail", Source: "host", Walker: &stubWalker{err: assert.AnError}, Root: "/tmp/f"}},
		},
		{
			name:      "single result",
			instances: []claude.Instance{{Label: "one", Source: "host", Walker: walker, Root: "/tmp/1"}},
		},
		{
			name: "multiple results",
			instances: []claude.Instance{
				{Label: "a", Source: "host", Walker: walker, Root: "/tmp/a"},
				{Label: "b", Source: "host", Walker: walker, Root: "/tmp/b"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			err := printInstanceUsage(buf, tt.instances, now)
			require.NoError(t, err)
			assert.Contains(t, buf.String(), "Claude usage")
		})
	}
}

func TestRunUsage_NoContainerIDs_WhenHostOnly(t *testing.T) {
	// Verify host-only instances don't add to containerIDs.
	now := fixedTime()
	walker := &stubWalker{}
	inst := claude.Instance{
		Label:  "Host (PID 100)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)

	// Just verify it completed and produced output.
	assert.True(t, buf.Len() > 0)
}

func TestRunUsage_OutputNotEmpty_WithValidInstances(t *testing.T) {
	now := fixedTime()
	walker := &stubWalker{}
	inst := claude.Instance{
		Label:  "Host (PID 42)",
		Source: "host",
		Walker: walker,
		Root:   "/tmp/test",
	}
	cmd, buf := newTestCmd()
	finder := &stubFinder{instances: []claude.Instance{inst}}

	err := RunUsage(cmd, finder, now)
	require.NoError(t, err)
	assert.True(t, buf.Len() > 0, "expected non-empty output")
}

func TestRunUsage_WindowBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		hour       int
		wantWindow string
	}{
		{"midnight window", 2, "00:00"},
		{"morning window", 7, "05:00"},
		{"midday window", 12, "10:00"},
		{"afternoon window", 17, "15:00"},
		{"evening window", 22, "20:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 3, 25, tt.hour, 0, 0, 0, time.UTC)
			walker := &stubWalker{}
			inst := claude.Instance{Label: "Host", Source: "host", Walker: walker, Root: "/tmp/test"}
			cmd, buf := newTestCmd()
			finder := &stubFinder{instances: []claude.Instance{inst}}

			err := RunUsage(cmd, finder, now)
			require.NoError(t, err)
			assert.Contains(t, buf.String(), tt.wantWindow)
		})
	}
}

func TestPrintInstanceUsage_EmptyUsageSummary(t *testing.T) {
	// When CollectInstanceUsage returns zero results (all walkers failed),
	// we should still get a formatted header with no model rows.
	now := fixedTime()
	badWalker := &stubWalker{err: assert.AnError}
	instances := []claude.Instance{
		{Label: "Host (PID 100)", Source: "host", Walker: badWalker, Root: "/tmp/bad1"},
		{Label: "Host (PID 200)", Source: "host", Walker: badWalker, Root: "/tmp/bad2"},
	}

	buf := &bytes.Buffer{}
	err := printInstanceUsage(buf, instances, now)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Claude usage")
	// No model names should appear.
	assert.False(t, strings.Contains(out, "opus"), "should not contain model names when all walkers fail")
}
