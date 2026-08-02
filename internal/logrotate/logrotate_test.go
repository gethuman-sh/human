package logrotate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestRotateMissingFileIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	rotated, err := Rotate(path, Policy{MaxSizeBytes: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotated {
		t.Fatal("missing file should not rotate")
	}
}

func TestRotateBelowThresholdIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	writeFile(t, path, "short")
	rotated, err := Rotate(path, Policy{MaxSizeBytes: 1024})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotated {
		t.Fatal("file below threshold should not rotate")
	}
	if got := readFile(t, path); got != "short" {
		t.Fatalf("live file changed: %q", got)
	}
}

func TestRotateDisabledWhenMaxSizeNonPositive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	writeFile(t, path, strings.Repeat("x", 100))
	rotated, err := Rotate(path, Policy{MaxSizeBytes: 0, MaxGenerations: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotated {
		t.Fatal("non-positive MaxSizeBytes should disable rotation")
	}
}

func TestRotateAtThresholdMovesContentAndTruncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writeFile(t, path, "line1\nline2\n")

	rotated, err := Rotate(path, Policy{MaxSizeBytes: 5, MaxGenerations: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rotated {
		t.Fatal("file at/above threshold should rotate")
	}
	if got := readFile(t, path); got != "" {
		t.Fatalf("live file should be truncated, got %q", got)
	}
	if got := readFile(t, path+".1"); got != "line1\nline2\n" {
		t.Fatalf(".1 should hold prior contents, got %q", got)
	}
}

// After rotation an O_APPEND writer keeps writing to the same truncated inode.
func TestRotatedLiveFileKeepsSameInodeForAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(strings.Repeat("a", 200)); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Rotate(path, Policy{MaxSizeBytes: 100, MaxGenerations: 3}); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// The still-open descriptor writes after truncation; O_APPEND lands the bytes
	// at the new end of file rather than at the pre-truncation offset.
	if _, err := f.WriteString("after\n"); err != nil {
		t.Fatalf("write after rotate: %v", err)
	}
	if got := readFile(t, path); got != "after\n" {
		t.Fatalf("post-rotation append should start a fresh file, got %q", got)
	}
}

func TestRotateShiftsGenerationsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	policy := Policy{MaxSizeBytes: 1, MaxGenerations: 5}
	for i := 1; i <= 3; i++ {
		writeFile(t, path, fmt.Sprintf("gen%d\n", i))
		if _, err := Rotate(path, policy); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
	}
	// Newest rotation is .1, oldest is .3.
	if got := readFile(t, path+".1"); got != "gen3\n" {
		t.Fatalf(".1 = %q, want gen3", got)
	}
	if got := readFile(t, path+".2"); got != "gen2\n" {
		t.Fatalf(".2 = %q, want gen2", got)
	}
	if got := readFile(t, path+".3"); got != "gen1\n" {
		t.Fatalf(".3 = %q, want gen1", got)
	}
}

func TestRotateDiscardsBeyondCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	policy := Policy{MaxSizeBytes: 1, MaxGenerations: 2}
	for i := 1; i <= 4; i++ {
		writeFile(t, path, fmt.Sprintf("gen%d\n", i))
		if _, err := Rotate(path, policy); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
	}
	if got := readFile(t, path+".1"); got != "gen4\n" {
		t.Fatalf(".1 = %q, want gen4", got)
	}
	if got := readFile(t, path+".2"); got != "gen3\n" {
		t.Fatalf(".2 = %q, want gen3", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf(".3 should have been discarded beyond the cap, stat err=%v", err)
	}
}

func TestRotateUnlimitedNeverDiscards(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")

	policy := Policy{MaxSizeBytes: 1, MaxGenerations: 0} // never delete
	const rounds = 6
	for i := 1; i <= rounds; i++ {
		writeFile(t, path, fmt.Sprintf("event%d\n", i))
		if _, err := Rotate(path, policy); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
	}
	// Every generation survives; .1 is newest, .rounds is the very first event.
	if got := readFile(t, path+".1"); got != fmt.Sprintf("event%d\n", rounds) {
		t.Fatalf(".1 = %q, want event%d", got, rounds)
	}
	if got := readFile(t, fmt.Sprintf("%s.%d", path, rounds)); got != "event1\n" {
		t.Fatalf(".%d = %q, want event1", rounds, got)
	}
}

func TestRotatePreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 50)), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Rotate(path, Policy{MaxSizeBytes: 10, MaxGenerations: 3}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	info, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("stat .1: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("rotated generation perm = %o, want 640", perm)
	}
}

func TestRotateReturnsErrorWhenGenerationCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writeFile(t, path, strings.Repeat("x", 50))
	// A directory sitting where .1 must be written makes the copy fail; the live
	// file must be left intact rather than truncated on a failed rotation.
	if err := os.Mkdir(path+".1", 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rotated, err := Rotate(path, Policy{MaxSizeBytes: 10, MaxGenerations: 3})
	if err == nil {
		t.Fatal("expected error when .1 cannot be created")
	}
	if rotated {
		t.Fatal("failed rotation must report rotated=false")
	}
	if got := readFile(t, path); got != strings.Repeat("x", 50) {
		t.Fatalf("live file must be untouched on failed rotation, got %q", got)
	}
}

func TestRotateDiscardsMultipleStrayGenerationsWhenCapShrinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writeFile(t, path, strings.Repeat("x", 50))
	// Five generations left by a previous, larger cap.
	for n := 1; n <= 5; n++ {
		writeFile(t, path+"."+fmt.Sprint(n), fmt.Sprintf("old%d", n))
	}
	if _, err := Rotate(path, Policy{MaxSizeBytes: 10, MaxGenerations: 2}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// Cap 2: only .1 (fresh) and .2 remain; .3..onwards are gone.
	for _, n := range []int{3, 4, 5} {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, n)); !os.IsNotExist(err) {
			t.Fatalf(".%d should be discarded when the cap shrinks, err=%v", n, err)
		}
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf(".2 should remain: %v", err)
	}
}

func TestHighestGenerationOnMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "daemon.log")
	if got := highestGeneration(path); got != 0 {
		t.Fatalf("highestGeneration on missing dir = %d, want 0", got)
	}
}

func TestHighestGenerationScansGaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writeFile(t, path, "live")
	writeFile(t, path+".1", "a")
	writeFile(t, path+".4", "b") // gap: no .2/.3
	writeFile(t, path+".notanumber", "c")

	if got := highestGeneration(path); got != 4 {
		t.Fatalf("highestGeneration = %d, want 4", got)
	}
}
