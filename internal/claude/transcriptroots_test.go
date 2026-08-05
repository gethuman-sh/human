package claude

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rootWalker serves different lines per root and can fail a chosen root, which
// is what a multi-root scan has to be tested against — the shared fakeWalker,
// ByteWalker and countingWalker all ignore the root they are handed.
type rootWalker struct {
	lines  map[string][][]byte
	failOn map[string]bool
	calls  map[string]int
}

func (r *rootWalker) WalkJSONL(root string, fn func(line []byte) error) error {
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[root]++
	if r.failOn[root] {
		return errors.New("walk failed")
	}
	for _, l := range r.lines[root] {
		if err := fn(l); err != nil {
			return err
		}
	}
	return nil
}

// resolved mirrors what TranscriptRoots does to a candidate path, so expected
// values compare against the same canonical form (the test tmpdir is itself
// under a symlinked /tmp on some systems).
func resolved(t *testing.T, path string) string {
	t.Helper()
	return resolveRoot(path)
}

// writeJSONL writes one assistant usage line into dir/<name>.jsonl, creating dir.
func writeJSONL(t *testing.T, dir, name string, line []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTranscriptRoots_hostOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := TranscriptRoots(nil)
	want := []string{resolved(t, filepath.Join(home, ".claude", "projects"))}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("TranscriptRoots(nil) = %v, want %v", got, want)
	}
}

func TestTranscriptRoots_includesEachProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p1 := t.TempDir()
	p2 := t.TempDir()

	got := TranscriptRoots([]string{p1, p2})

	// Order is contractual: costs are float64 and float addition is not
	// associative, so a stable fold order keeps totals reproducible.
	want := []string{
		resolved(t, filepath.Join(home, ".claude", "projects")),
		resolved(t, AgentTranscriptRoot(p1)),
		resolved(t, AgentTranscriptRoot(p2)),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d roots %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("root %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTranscriptRoots_targetsProjectsSubtreeOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := t.TempDir()
	claudeDir := filepath.Join(p, ".devcontainer", "claude")
	creds := filepath.Join(claudeDir, ".credentials.json")

	got := TranscriptRoots([]string{p})

	for _, root := range got {
		if root == resolved(t, filepath.Join(home, ".claude", "projects")) {
			continue
		}
		if !strings.HasSuffix(root, filepath.Join(".devcontainer", "claude", "projects")) {
			t.Errorf("agent root %q does not target the projects/ subtree", root)
		}
		if root == resolveRoot(claudeDir) {
			t.Errorf("root %q is the claude dir itself, which holds credentials", root)
		}
		if pathWithin(resolveRoot(creds), root) {
			t.Errorf("credentials file lies under walked root %q", root)
		}
	}
}

func TestTranscriptRoots_dedupesRepeatedProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := t.TempDir()

	got := TranscriptRoots([]string{p, p})
	if len(got) != 2 {
		t.Fatalf("got %d roots %v, want 2 (host + one agent root)", len(got), got)
	}
}

func TestTranscriptRoots_dedupesSymlinkedProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, ".devcontainer", "claude", "projects"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	got := TranscriptRoots([]string{real, link})
	if len(got) != 2 {
		t.Fatalf("got %d roots %v, want 2 — the symlinked spelling must dedupe", len(got), got)
	}
	want := resolved(t, AgentTranscriptRoot(real))
	if got[1] != want {
		t.Errorf("agent root = %q, want the resolved form %q", got[1], want)
	}
}

func TestTranscriptRoots_resolvesSymlinkedRootSoItIsWalkable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	p := filepath.Join(base, "project")
	target := filepath.Join(base, "elsewhere")
	now := time.Now().UTC()
	writeJSONL(t, filepath.Join(target, "session"), "a.jsonl",
		makeLine(t, "assistant", "claude-opus-4-5", now, 10, 100, 0, 0))

	if err := os.MkdirAll(filepath.Join(p, ".devcontainer", "claude"), 0o750); err != nil {
		t.Fatal(err)
	}
	// The final component is the symlink — the case filepath.Walk cannot follow.
	if err := os.Symlink(target, AgentTranscriptRoot(p)); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	roots := TranscriptRoots([]string{p})
	agentRoot := roots[len(roots)-1]
	if agentRoot != resolveRoot(target) {
		t.Fatalf("agent root = %q, want the resolved target %q", agentRoot, resolveRoot(target))
	}

	scan := ScanTokensRoots(OSDirWalker{}, []string{agentRoot}, now.Add(-time.Hour), now.Add(time.Hour), now)
	if scan.WindowOutput != 100 {
		t.Errorf("WindowOutput = %d, want 100 — a symlinked root must still be walked", scan.WindowOutput)
	}
}

func TestTranscriptRoots_dropsNestedRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hostRoot := filepath.Join(home, ".claude", "projects")
	nested := filepath.Join(hostRoot, "checkout")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}

	got := TranscriptRoots([]string{nested})
	if len(got) != 1 || got[0] != resolved(t, hostRoot) {
		t.Fatalf("got %v, want only the host root %q — a nested root is walked twice", got, resolved(t, hostRoot))
	}
}

func TestTranscriptRoots_skipsEmptyProjectDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := t.TempDir()

	got := TranscriptRoots([]string{"", p})
	if len(got) != 2 {
		t.Fatalf("got %d roots %v, want 2", len(got), got)
	}
	for _, r := range got {
		if r == "" {
			t.Error("empty root returned")
		}
	}
}

func TestTranscriptRoots_noHomeSkipsHostRoot(t *testing.T) {
	t.Setenv("HOME", "")
	p := t.TempDir()

	got := TranscriptRoots([]string{p})
	if len(got) != 1 {
		t.Fatalf("got %d roots %v, want 1 (agent root only)", len(got), got)
	}
	if got[0] != resolved(t, AgentTranscriptRoot(p)) {
		t.Errorf("root = %q, want %q", got[0], resolved(t, AgentTranscriptRoot(p)))
	}
}

func TestTranscriptRoots_missingProjectDirStillReturned(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	gone := filepath.Join(t.TempDir(), "gone")

	got := TranscriptRoots([]string{gone})
	if len(got) != 2 {
		t.Fatalf("got %d roots %v, want 2 — a not-yet-created root is kept", len(got), got)
	}
	if got[1] != AgentTranscriptRoot(gone) {
		t.Errorf("root = %q, want the unresolved absolute form %q", got[1], AgentTranscriptRoot(gone))
	}
}

func TestAgentTranscriptRoot_empty(t *testing.T) {
	if got := AgentTranscriptRoot(""); got != "" {
		t.Errorf("AgentTranscriptRoot(\"\") = %q, want \"\" — a join would yield a relative path", got)
	}
}

func TestPathWithin_boundaries(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         bool
	}{
		{"/a/bc", "/a/b", false},
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b", false},
		{"/a", "/", true},
	}
	for _, c := range cases {
		if got := pathWithin(c.path, c.prefix); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", c.path, c.prefix, got, c.want)
		}
	}
}

func TestResolveRoot_keepsMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never", "created")
	if got := resolveRoot(missing); got != missing {
		t.Errorf("resolveRoot(%q) = %q, want the absolute form unchanged", missing, got)
	}
}

func TestScanTokensRoots_sumsAcrossRoots(t *testing.T) {
	// Both lines land in the same UTC hour; bucket keys are UTC-derived, so the
	// merge needs no timezone handling.
	ts := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	now := time.Date(2026, 3, 25, 11, 45, 0, 0, time.UTC)
	walker := &rootWalker{lines: map[string][][]byte{
		"/a": {makeLine(t, "assistant", "claude-opus-4-5", ts, 10, 100, 0, 0)},
		"/b": {makeLine(t, "assistant", "claude-sonnet-4-5", ts, 20, 300, 0, 0)},
	}}

	scan := ScanTokensRoots(walker, []string{"/a", "/b"}, ts.Add(-time.Hour), now, now)

	if scan.WindowOutput != 400 {
		t.Errorf("WindowOutput = %d, want 400", scan.WindowOutput)
	}
	if scan.WindowInput != 30 {
		t.Errorf("WindowInput = %d, want 30", scan.WindowInput)
	}
	if len(scan.PerHour) != 1 {
		t.Fatalf("got %d buckets %+v, want 1", len(scan.PerHour), scan.PerHour)
	}
	if want := "2026-03-25 11:00"; scan.PerHour[0].Bucket != want {
		t.Errorf("bucket = %q, want %q", scan.PerHour[0].Bucket, want)
	}
	if scan.PerHour[0].Output != 400 {
		t.Errorf("bucket output = %d, want 400", scan.PerHour[0].Output)
	}
	if len(scan.ByModel) != 2 {
		t.Fatalf("got %d models %+v, want 2", len(scan.ByModel), scan.ByModel)
	}
	if scan.ByModel[0].CostUSD < scan.ByModel[1].CostUSD {
		t.Errorf("ByModel not sorted by cost desc: %+v", scan.ByModel)
	}
}

func TestScanTokensRoots_perRootErrorDegrades(t *testing.T) {
	ts := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	walker := &rootWalker{
		lines:  map[string][][]byte{"/a": {makeLine(t, "assistant", "claude-opus-4-5", ts, 10, 100, 0, 0)}},
		failOn: map[string]bool{"/b": true},
	}

	scan := ScanTokensRoots(walker, []string{"/a", "/b"}, ts.Add(-time.Hour), ts.Add(time.Hour), ts)

	if scan.WindowOutput != 100 {
		t.Errorf("WindowOutput = %d, want 100 — a failing root must not empty the panel", scan.WindowOutput)
	}
}

func TestScanTokensRoots_walksEachRootOnce(t *testing.T) {
	ts := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	walker := &rootWalker{}

	ScanTokensRoots(walker, []string{"/a", "/b", "/c"}, ts.Add(-time.Hour), ts.Add(time.Hour), ts)

	for _, root := range []string{"/a", "/b", "/c"} {
		if walker.calls[root] != 1 {
			t.Errorf("root %s walked %d times, want exactly 1", root, walker.calls[root])
		}
	}
}

func TestScanTokensRoots_emptyRoots(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)

	scan := ScanTokensRoots(&rootWalker{}, nil, now.Add(-time.Hour), now, now)

	if scan.WindowOutput != 0 || scan.WindowCostUSD != 0 {
		t.Errorf("want a zero scan, got %+v", scan)
	}
	if scan.PerHour == nil || len(scan.PerHour) != 0 {
		t.Errorf("PerHour = %+v, want non-nil empty", scan.PerHour)
	}
	if scan.ByModel == nil || len(scan.ByModel) != 0 {
		t.Errorf("ByModel = %+v, want non-nil empty", scan.ByModel)
	}
}

func TestScanTokensRoots_matchesSingleRootScan(t *testing.T) {
	ts := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	lines := [][]byte{
		makeLine(t, "assistant", "claude-opus-4-5", ts, 10, 100, 5, 7),
		makeLine(t, "assistant", "claude-sonnet-4-5", ts.Add(time.Hour), 20, 200, 0, 0),
	}
	walker := &rootWalker{lines: map[string][][]byte{"/a": lines}}
	since, until := ts.Add(-time.Hour), ts.Add(2*time.Hour)

	single, err := ScanTokens(walker, "/a", since, until, ts)
	if err != nil {
		t.Fatal(err)
	}
	folded := ScanTokensRoots(walker, []string{"/a"}, since, until, ts)

	if folded.WindowOutput != single.WindowOutput || folded.WindowCostUSD != single.WindowCostUSD {
		t.Errorf("folded window %+v differs from single-root %+v", folded, single)
	}
	if len(folded.PerHour) != len(single.PerHour) || len(folded.ByModel) != len(single.ByModel) {
		t.Errorf("folded shape %+v differs from single-root %+v", folded, single)
	}
	for i := range single.ByModel {
		if folded.ByModel[i] != single.ByModel[i] {
			t.Errorf("ByModel[%d] = %+v, want %+v", i, folded.ByModel[i], single.ByModel[i])
		}
	}
}

func TestScanTokensRoots_missingRootOnRealFS(t *testing.T) {
	now := time.Now().UTC()
	real := t.TempDir()
	writeJSONL(t, filepath.Join(real, "session"), "a.jsonl",
		makeLine(t, "assistant", "claude-opus-4-5", now, 10, 100, 0, 0))
	missing := filepath.Join(t.TempDir(), "nonexistent", "xyz")

	scan := ScanTokensRoots(OSDirWalker{}, []string{real, missing}, now.Add(-time.Hour), now.Add(time.Hour), now)

	if scan.WindowOutput != 100 {
		t.Errorf("WindowOutput = %d, want 100 — a missing root degrades to empty", scan.WindowOutput)
	}
}

func TestCalculateUsageRoots_mergesModels(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	walker := &rootWalker{lines: map[string][][]byte{
		"/a": {makeLine(t, "assistant", "claude-opus-4-5", now, 100, 0, 0, 0)},
		"/b": {
			makeLine(t, "assistant", "claude-opus-4-5", now, 50, 0, 0, 0),
			makeLine(t, "assistant", "claude-sonnet-4-5", now, 10, 0, 0, 0),
		},
	}}

	merged := CalculateUsageRoots(walker, []string{"/a", "/b"}, now)

	opus := must(t, merged.Models["opus 4.5"], "opus totals missing from merge")
	if opus.InputTokens != 150 {
		t.Errorf("opus input = %d, want 150", opus.InputTokens)
	}
	if _, ok := merged.Models["sonnet 4.5"]; !ok {
		t.Errorf("sonnet missing from merge: %+v", merged.Models)
	}
}

func TestCalculateUsageRoots_perRootErrorDegrades(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	walker := &rootWalker{
		lines:  map[string][][]byte{"/a": {makeLine(t, "assistant", "claude-opus-4-5", now, 100, 0, 0, 0)}},
		failOn: map[string]bool{"/b": true},
	}

	merged := CalculateUsageRoots(walker, []string{"/a", "/b"}, now)

	if merged.Models == nil {
		t.Fatal("Models map is nil")
	}
	opus := must(t, merged.Models["opus 4.5"], "opus totals missing")
	if opus.InputTokens != 100 {
		t.Errorf("opus input = %d, want 100", opus.InputTokens)
	}
}

func TestCalculateUsageRoots_noRoots(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)

	merged := CalculateUsageRoots(&rootWalker{}, nil, now)

	if merged == nil || merged.Models == nil {
		t.Fatal("want a non-nil summary with a non-nil Models map, safe for FormatUsage")
	}
	if len(merged.Models) != 0 {
		t.Errorf("Models = %+v, want empty", merged.Models)
	}
}
