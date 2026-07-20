package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func open(t *testing.T) Workspace {
	t.Helper()
	w, err := Open(t.TempDir(), "bugs")
	require.NoError(t, err)
	return w
}

func TestOpen_createsWorkspaceDir(t *testing.T) {
	w := open(t)
	info, err := os.Stat(w.Root())
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestOpen_rejectsBadName(t *testing.T) {
	_, err := Open(t.TempDir(), "../escape")
	require.Error(t, err)
	_, err = Open(t.TempDir(), "Bad Name")
	require.Error(t, err)
}

func TestAppend_allocatesSequentialIDs(t *testing.T) {
	w := open(t)

	id1, dup, err := w.Append(Finding{File: "a.go", Line: 10, Category: "logic", Title: "first"})
	require.NoError(t, err)
	assert.False(t, dup)
	assert.Equal(t, "C-001", id1)

	id2, dup, err := w.Append(Finding{File: "b.go", Line: 20, Category: "logic", Title: "second", Body: "details"})
	require.NoError(t, err)
	assert.False(t, dup)
	assert.Equal(t, "C-002", id2)

	content, err := os.ReadFile(w.CandidatesPath())
	require.NoError(t, err)
	assert.Contains(t, string(content), "### C-001: first")
	assert.Contains(t, string(content), "- location: b.go:20 (logic)")
	assert.Contains(t, string(content), "details")
}

func TestAppend_dropsExactDuplicates(t *testing.T) {
	w := open(t)

	id1, _, err := w.Append(Finding{File: "a.go", Line: 10, Category: "logic", Title: "first"})
	require.NoError(t, err)

	id2, dup, err := w.Append(Finding{File: "a.go", Line: 10, Category: "logic", Title: "same place"})
	require.NoError(t, err)
	assert.True(t, dup)
	assert.Equal(t, id1, id2, "duplicate reports the surviving ID")

	count, err := w.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestAppend_sameLineDifferentCategoryIsNotDuplicate(t *testing.T) {
	w := open(t)
	_, _, err := w.Append(Finding{File: "a.go", Line: 10, Category: "logic", Title: "x"})
	require.NoError(t, err)
	_, dup, err := w.Append(Finding{File: "a.go", Line: 10, Category: "security", Title: "y"})
	require.NoError(t, err)
	assert.False(t, dup)
}

func TestAppend_parallelAgentsGetUniqueIDs(t *testing.T) {
	w := open(t)
	const n = 20
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, _, err := w.Append(Finding{File: "f.go", Line: i, Category: "logic", Title: "t"})
			require.NoError(t, err)
			ids[i] = id
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range ids {
		assert.False(t, seen[id], "duplicate ID %s allocated", id)
		seen[id] = true
	}
	count, err := w.Count()
	require.NoError(t, err)
	assert.Equal(t, n, count)
}

func TestCount_emptyWorkspace(t *testing.T) {
	w := open(t)
	count, err := w.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestState_getSetReplace(t *testing.T) {
	w := open(t)

	value, err := w.StateGet("iterations")
	require.NoError(t, err)
	assert.Empty(t, value)

	require.NoError(t, w.StateSet("iterations", "1"))
	require.NoError(t, w.StateSet("status", "running"))
	require.NoError(t, w.StateSet("iterations", "2"))

	value, err = w.StateGet("iterations")
	require.NoError(t, err)
	assert.Equal(t, "2", value)
	value, err = w.StateGet("status")
	require.NoError(t, err)
	assert.Equal(t, "running", value)
}

func TestStateSet_rejectsMultiline(t *testing.T) {
	w := open(t)
	assert.Error(t, w.StateSet("key", "a\nb"))
	assert.Error(t, w.StateSet("bad:key", "v"))
}

func TestReportPath_timestamped(t *testing.T) {
	w := open(t)
	ts := time.Date(2026, 7, 20, 21, 4, 5, 0, time.UTC)
	assert.Equal(t, filepath.Join(w.Root(), "bugs-20260720-210405.md"), w.ReportPath(ts))
}

func TestCleanup_removesDotFilesKeepsReports(t *testing.T) {
	w := open(t)
	_, _, err := w.Append(Finding{File: "a.go", Line: 1, Category: "logic", Title: "x"})
	require.NoError(t, err)
	require.NoError(t, w.StateSet("k", "v"))
	report := w.ReportPath(time.Now())
	require.NoError(t, os.WriteFile(report, []byte("final"), 0o600))

	require.NoError(t, w.Cleanup())

	entries, err := os.ReadDir(w.Root())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.False(t, strings.HasPrefix(entries[0].Name(), "."))
}

func TestCleanup_missingWorkspaceIsFine(t *testing.T) {
	w := Workspace{Dir: t.TempDir(), Name: "never-opened"}
	assert.NoError(t, w.Cleanup())
}

func TestNextID_skipsMergedGaps(t *testing.T) {
	assert.Equal(t, "C-001", nextID(""))
	assert.Equal(t, "C-043", nextID("### C-007: a\n### C-042: b\n"))
}
