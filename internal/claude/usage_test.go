package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeWalker replays pre-built JSONL lines.
type fakeWalker struct {
	lines [][]byte
}

func (f fakeWalker) WalkJSONL(_ string, fn func(line []byte) error) error {
	for _, l := range f.lines {
		if err := fn(l); err != nil {
			return err
		}
	}
	return nil
}

func makeLine(t *testing.T, typ, model string, ts time.Time, input, output, cacheCreate, cacheRead int) []byte {
	t.Helper()
	m := map[string]interface{}{
		"type":      typ,
		"timestamp": ts.Format(time.RFC3339),
		"message": map[string]interface{}{
			"model": model,
			"usage": map[string]int{
				"input_tokens":                input,
				"output_tokens":               output,
				"cache_creation_input_tokens": cacheCreate,
				"cache_read_input_tokens":     cacheRead,
			},
		},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWindowStart(t *testing.T) {
	tests := []struct {
		hour     int
		wantHour int
	}{
		{0, 0}, {3, 0}, {4, 0},
		{5, 5}, {7, 5}, {9, 5},
		{10, 10}, {14, 10},
		{15, 15}, {19, 15},
		{20, 20}, {23, 20},
	}
	for _, tt := range tests {
		now := time.Date(2026, 3, 20, tt.hour, 30, 0, 0, time.UTC)
		got := WindowStart(now)
		if got.Hour() != tt.wantHour {
			t.Errorf("WindowStart(hour=%d) = %d, want %d", tt.hour, got.Hour(), tt.wantHour)
		}
	}
}

func TestCalculateUsage(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	inWindow := time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC)
	outOfWindow := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

	lines := [][]byte{
		makeLine(t, "assistant", "claude-sonnet-4-5-20250929", inWindow, 1_000_000, 0, 0, 0),
		makeLine(t, "assistant", "claude-opus-4-6", inWindow, 0, 1_000_000, 0, 0),
		// Out of window — should be ignored
		makeLine(t, "assistant", "claude-sonnet-4-5-20250929", outOfWindow, 1_000_000, 0, 0, 0),
		// Wrong type — should be ignored
		makeLine(t, "human", "claude-sonnet-4-5-20250929", inWindow, 1_000_000, 0, 0, 0),
		// Malformed line — should be skipped
		[]byte(`{invalid json`),
	}

	w := fakeWalker{lines: lines}
	summary, err := CalculateUsage(w, "/fake", now)
	if err != nil {
		t.Fatal(err)
	}

	sonnet := summary.Models["sonnet 4.5"]
	if sonnet == nil {
		t.Fatal("expected sonnet 4.5 model entry")
	}
	if sonnet.InputTokens != 1_000_000 {
		t.Errorf("sonnet input = %d, want 1000000", sonnet.InputTokens)
	}
	opus := summary.Models["opus 4.6"]
	if opus == nil {
		t.Fatal("expected opus 4.6 model entry")
	}
	if opus.OutputTokens != 1_000_000 {
		t.Errorf("opus output = %d, want 1000000", opus.OutputTokens)
	}
}

func TestCalculateUsageCacheTokens(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	inWindow := time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC)

	lines := [][]byte{
		makeLine(t, "assistant", "claude-sonnet-4-5-20250929", inWindow, 0, 0, 1_000_000, 1_000_000),
	}

	w := fakeWalker{lines: lines}
	summary, err := CalculateUsage(w, "/fake", now)
	if err != nil {
		t.Fatal(err)
	}
	sonnet := summary.Models["sonnet 4.5"]
	if sonnet == nil {
		t.Fatal("expected sonnet 4.5 model entry")
	}
	if sonnet.CacheCreate != 1_000_000 {
		t.Errorf("sonnet cache_create = %d, want 1000000", sonnet.CacheCreate)
	}
	if sonnet.CacheRead != 1_000_000 {
		t.Errorf("sonnet cache_read = %d, want 1000000", sonnet.CacheRead)
	}
}

func TestFormatUsage(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	summary := &UsageSummary{
		Models: map[string]*ModelUsage{
			"sonnet 4.5": {InputTokens: 1_000_000, OutputTokens: 500_000},
			"opus 4.6":   {OutputTokens: 1_000_000},
		},
	}
	var buf bytes.Buffer
	err := FormatUsage(&buf, summary, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "opus 4.6") {
		t.Errorf("should contain opus 4.6, got: %s", got)
	}
	if !strings.Contains(got, "sonnet 4.5") {
		t.Errorf("should contain sonnet 4.5, got: %s", got)
	}
	if !strings.Contains(got, "10:00") {
		t.Errorf("should contain window start, got: %s", got)
	}
	if !strings.Contains(got, "1.0M") {
		t.Errorf("should contain formatted tokens, got: %s", got)
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{500, "500"},
		{1_500, "1.5K"},
		{1_500_000, "1.5M"},
		{0, "0"},
	}
	for _, tt := range tests {
		got := FormatTokens(tt.n)
		if got != tt.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestClassifyModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-6", "opus 4.6"},
		{"claude-opus-4-5-20251101", "opus 4.5"},
		{"claude-opus-4-20250514", "opus"},
		{"claude-sonnet-4-6", "sonnet 4.6"},
		{"claude-sonnet-4-5-20250929", "sonnet 4.5"},
		{"claude-sonnet-4-20250514", "sonnet"},
		{"claude-haiku-4-5-20251001", "haiku 4.5"},
		{"claude-haiku-3-5-20241022", "haiku 3.5"},
		{"sonnet", "sonnet"},
		{"haiku", "haiku"},
		{"some-unknown-model", "sonnet"},
	}
	for _, tt := range tests {
		got := classifyModel(tt.model)
		if got != tt.want {
			t.Errorf("classifyModel(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestFormatUsageEmpty(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	summary := &UsageSummary{Models: map[string]*ModelUsage{}}
	var buf bytes.Buffer
	err := FormatUsage(&buf, summary, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "Claude usage") {
		t.Errorf("empty summary should show header, got: %s", got)
	}
}

func TestMergeUsage(t *testing.T) {
	dst := &UsageSummary{Models: map[string]*ModelUsage{
		"opus 4.6": {InputTokens: 100, OutputTokens: 50},
	}}
	src := &UsageSummary{Models: map[string]*ModelUsage{
		"opus 4.6":   {InputTokens: 200, OutputTokens: 100, CacheCreate: 10},
		"sonnet 4.5": {InputTokens: 300},
	}}

	MergeUsage(dst, src)

	opus := dst.Models["opus 4.6"]
	if opus == nil {
		t.Fatal("expected opus 4.6 in dst after merge")
	}
	if opus.InputTokens != 300 {
		t.Errorf("opus input = %d, want 300", opus.InputTokens)
	}
	if opus.OutputTokens != 150 {
		t.Errorf("opus output = %d, want 150", opus.OutputTokens)
	}
	if opus.CacheCreate != 10 {
		t.Errorf("opus cache_create = %d, want 10", opus.CacheCreate)
	}
	sonnet := dst.Models["sonnet 4.5"]
	if sonnet == nil {
		t.Fatal("expected sonnet 4.5 in dst after merge")
	}
	if sonnet.InputTokens != 300 {
		t.Errorf("sonnet input = %d, want 300", sonnet.InputTokens)
	}
}

func TestMergeUsage_NilModels(t *testing.T) {
	dst := &UsageSummary{Models: map[string]*ModelUsage{}}
	src := &UsageSummary{Models: map[string]*ModelUsage{
		"opus 4.6": nil,
	}}

	MergeUsage(dst, src)

	if len(dst.Models) != 0 {
		t.Errorf("expected 0 models, got %d", len(dst.Models))
	}
}

func TestFormatMultiUsage(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	instances := []InstanceUsage{
		{
			Instance: Instance{Label: "Host (PID 12345)", Source: "host"},
			Summary: &UsageSummary{Models: map[string]*ModelUsage{
				"sonnet 4.5": {InputTokens: 1_000_000, OutputTokens: 500_000},
			}},
			State: StateUnknown,
		},
		{
			Instance: Instance{
				Label:  `Container "dev-myapp" (abc123)`,
				Source: "container",
				Memory: &MemoryInfo{Usage: 512 * 1024 * 1024, Limit: 2 * 1024 * 1024 * 1024},
			},
			Summary: &UsageSummary{Models: map[string]*ModelUsage{
				"opus 4.6": {InputTokens: 500_000, OutputTokens: 200_000, CacheCreate: 100_000, CacheRead: 50_000},
			}},
			State: StateUnknown,
		},
	}

	var buf bytes.Buffer
	err := FormatMultiUsage(&buf, instances, now)
	if err != nil {
		t.Fatal(err)
	}

	got := buf.String()

	// Check header.
	if !strings.Contains(got, "Claude usage [10:00") {
		t.Errorf("should contain window header, got: %s", got)
	}

	// Check instance labels.
	if !strings.Contains(got, "Host (PID 12345)") {
		t.Errorf("should contain host label, got: %s", got)
	}
	if !strings.Contains(got, `Container "dev-myapp" (abc123)`) {
		t.Errorf("should contain container label, got: %s", got)
	}

	// Check Total section.
	if !strings.Contains(got, "Total:") {
		t.Errorf("should contain Total section, got: %s", got)
	}

	// Check that both models appear in Total.
	totalIdx := strings.Index(got, "Total:")
	totalSection := got[totalIdx:]
	if !strings.Contains(totalSection, "sonnet 4.5") {
		t.Errorf("Total should contain sonnet 4.5, got: %s", totalSection)
	}
	if !strings.Contains(totalSection, "opus 4.6") {
		t.Errorf("Total should contain opus 4.6, got: %s", totalSection)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		b    uint64
		want string
	}{
		{0, "0 MiB"},
		{256 * 1024 * 1024, "256 MiB"},
		{512 * 1024 * 1024, "512 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
		{2 * 1024 * 1024 * 1024, "2.0 GiB"},
		{3 * 1024 * 1024 * 1024 / 2, "1.5 GiB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.b)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	tests := []struct {
		name string
		mem  *MemoryInfo
		want string
	}{
		{"nil", nil, ""},
		{"usage only", &MemoryInfo{Usage: 512 * 1024 * 1024}, "mem: 512 MiB"},
		{"with limit", &MemoryInfo{Usage: 512 * 1024 * 1024, Limit: 2 * 1024 * 1024 * 1024}, "mem: 512 MiB / 2.0 GiB"},
		{"huge limit treated as unlimited", &MemoryInfo{Usage: 512 * 1024 * 1024, Limit: 1 << 62}, "mem: 512 MiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMemory(tt.mem)
			if got != tt.want {
				t.Errorf("FormatMemory() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatMultiUsage_Empty(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	err := FormatMultiUsage(&buf, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "Claude usage") {
		t.Errorf("should contain header, got: %s", got)
	}
	if !strings.Contains(got, "Total:") {
		t.Errorf("should contain Total, got: %s", got)
	}
}

func TestFormatMultiUsage_NilSummary(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	instances := []InstanceUsage{
		{
			Instance: Instance{Label: "Host (PID 111)", Source: "host"},
			Summary:  nil, // nil summary should be handled gracefully
			State:    StateUnknown,
		},
	}
	var buf bytes.Buffer
	err := FormatMultiUsage(&buf, instances, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "Host (PID 111)") {
		t.Errorf("should contain instance label, got: %s", got)
	}
	if !strings.Contains(got, "Total:") {
		t.Errorf("should contain Total section, got: %s", got)
	}
}

func TestIsVersionDigit(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"4", true},
		{"45", true},
		{"", false},
		{"123", false},       // too long
		{"20250514", false},  // date stamp, too long
		{"ab", false},        // non-digit
		{"4a", false},        // mixed
	}
	for _, tt := range tests {
		got := isVersionDigit(tt.s)
		if got != tt.want {
			t.Errorf("isVersionDigit(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestMergeUsage_NilDst(t *testing.T) {
	src := &UsageSummary{Models: map[string]*ModelUsage{
		"opus 4.6": {InputTokens: 100},
	}}
	// Should not panic when dst is nil.
	MergeUsage(nil, src)
}

func TestMergeUsage_NilSrc(t *testing.T) {
	dst := &UsageSummary{Models: map[string]*ModelUsage{
		"opus 4.6": {InputTokens: 100},
	}}
	MergeUsage(dst, nil)
	// dst should be unchanged.
	m := dst.Models["opus 4.6"]
	if m == nil || m.InputTokens != 100 {
		t.Errorf("dst should be unchanged after nil src merge")
	}
}

func TestFormatModelRows_SkipsZeroTotal(t *testing.T) {
	summary := &UsageSummary{Models: map[string]*ModelUsage{
		"sonnet 4.5": {InputTokens: 0, OutputTokens: 0, CacheCreate: 0, CacheRead: 0},
		"opus 4.6":   {InputTokens: 100, OutputTokens: 50},
	}}
	var buf bytes.Buffer
	err := FormatModelRows(&buf, summary, 150)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "sonnet 4.5") {
		t.Errorf("should skip zero-total model, got: %s", got)
	}
	if !strings.Contains(got, "opus 4.6") {
		t.Errorf("should contain non-zero model, got: %s", got)
	}
}

func TestFormatModelRows_ZeroGrandTotal(t *testing.T) {
	summary := &UsageSummary{Models: map[string]*ModelUsage{
		"opus 4.6": {InputTokens: 100, OutputTokens: 50},
	}}
	var buf bytes.Buffer
	err := FormatModelRows(&buf, summary, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// With grandTotal=0, pct should be 0.
	if !strings.Contains(got, "opus 4.6") {
		t.Errorf("should contain model, got: %s", got)
	}
	if !strings.Contains(got, "0%") {
		t.Errorf("should contain 0%% when grandTotal is 0, got: %s", got)
	}
}

func TestCollectInstanceUsage(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	inWindow := time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC)

	lines := [][]byte{
		makeLine(t, "assistant", "claude-opus-4-6", inWindow, 500, 200, 0, 0),
	}

	instances := []Instance{
		{
			Label:  "Test Instance",
			Source: "host",
			Walker: fakeWalker{lines: lines},
			Root:   "/fake",
		},
	}

	results := CollectInstanceUsage(instances, now)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Summary == nil {
		t.Fatal("expected non-nil summary")
	}
	opus := results[0].Summary.Models["opus 4.6"]
	if opus == nil {
		t.Fatal("expected opus 4.6 model entry")
	}
	if opus.InputTokens != 500 {
		t.Errorf("opus input = %d, want 500", opus.InputTokens)
	}
	if results[0].State != StateUnknown {
		t.Errorf("state = %v, want StateUnknown", results[0].State)
	}
}

func TestCollectInstanceUsage_WalkerError(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	// errorWalker always returns an error.
	instances := []Instance{
		{
			Label:  "Broken Instance",
			Source: "host",
			Walker: errorWalker{},
			Root:   "/fake",
		},
	}

	results := CollectInstanceUsage(instances, now)
	if len(results) != 0 {
		t.Errorf("expected 0 results when walker fails, got %d", len(results))
	}
}

// errorWalker always returns an error.
type errorWalker struct{}

func (errorWalker) WalkJSONL(_ string, _ func(line []byte) error) error {
	return errors.New("walk failed")
}

func TestFormatUsage_NilModelEntry(t *testing.T) {
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	summary := &UsageSummary{
		Models: map[string]*ModelUsage{
			"opus 4.6":   nil,
			"sonnet 4.5": {InputTokens: 1000},
		},
	}
	var buf bytes.Buffer
	err := FormatUsage(&buf, summary, now)
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// Should not crash on nil model entry and should still print sonnet.
	if !strings.Contains(got, "sonnet 4.5") {
		t.Errorf("should contain sonnet 4.5, got: %s", got)
	}
}

func TestModelUsageTotal(t *testing.T) {
	mu := &ModelUsage{InputTokens: 100, OutputTokens: 200, CacheCreate: 50, CacheRead: 30}
	got := mu.Total()
	want := 380
	if got != want {
		t.Errorf("ModelUsage.Total() = %d, want %d", got, want)
	}
}
