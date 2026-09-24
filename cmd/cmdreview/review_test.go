package cmdreview

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/recall"
)

// fakeReader records what it was asked and answers with what the test set.
type fakeReader struct {
	gotProject string
	gotFiles   []string
	gotLimit   int
	findings   []recall.ReviewFinding
	err        error
}

func (f *fakeReader) FindingsForFiles(_ context.Context, project string, files []string, limit int) ([]recall.ReviewFinding, error) {
	f.gotProject = project
	f.gotFiles = files
	f.gotLimit = limit
	return f.findings, f.err
}

func depsFor(r *fakeReader, project string) Deps {
	return Deps{
		DBPath:   func() string { return "unused" },
		NewStore: func(string) (recall.FindingsReader, error) { return r, nil },
		Project:  func(context.Context) string { return project },
	}
}

func TestRunFindings_printsClassTicketRoundDisposition(t *testing.T) {
	r := &fakeReader{findings: []recall.ReviewFinding{{
		Project: "p", Key: "SC-5123", PR: 512, Round: 2,
		File: "internal/daemon/pr_review_loop.go", Slug: "vocabulary not updated",
		Class: "dependents", Text: "the switch in board_retry.go reads the same vocabulary",
		Disposition: "done", Note: "escaped the input", RecordedAt: time.Now(),
	}}}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"internal/daemon/pr_review_loop.go"}, "SC-1", 20, false, depsFor(r, "p")))

	got := out.String()
	assert.Contains(t, got, "internal/daemon/pr_review_loop.go")
	assert.Contains(t, got, "[dependents]")
	assert.Contains(t, got, "SC-5123")
	assert.Contains(t, got, "PR 512 round 2")
	assert.Contains(t, got, "done")
	assert.Contains(t, got, "note: escaped the input")
	assert.Equal(t, "p", r.gotProject)
}

func TestRunFindings_unclassifiedIsNamed(t *testing.T) {
	r := &fakeReader{findings: []recall.ReviewFinding{{Key: "SC-1", File: "a.go", Slug: "x"}}}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"a.go"}, "", 20, false, depsFor(r, "")))
	assert.Contains(t, out.String(), "[unclassified]")
}

func TestRunFindings_normalizesThePathBeforeQuerying(t *testing.T) {
	r := &fakeReader{}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"Internal/Foo.go:42"}, "", 5, false, depsFor(r, "")))
	assert.Equal(t, []string{"internal/foo.go"}, r.gotFiles, "the reader must ask the way the writer wrote")
	assert.Equal(t, 5, r.gotLimit)
}

func TestRunFindings_emptyIsNotAnError(t *testing.T) {
	r := &fakeReader{}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"a.go"}, "", 20, false, depsFor(r, "")))
	assert.Equal(t, "No prior findings recorded for these files.\n", out.String())
}

func TestRunFindings_storeErrorIsAnError(t *testing.T) {
	r := &fakeReader{err: errors.WithDetails("db is gone")}
	var out bytes.Buffer
	err := RunFindings(context.Background(), &out, []string{"a.go"}, "", 20, false, depsFor(r, ""))
	require.Error(t, err, "an unreadable record is never an empty answer")
	assert.Empty(t, out.String())
}

func TestRunFindings_jsonEmptyIsAList(t *testing.T) {
	r := &fakeReader{}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"a.go"}, "", 20, true, depsFor(r, "")))
	assert.Equal(t, "[]\n", out.String(), "null would read as a malformed answer")
}

func TestRunFindings_jsonCarriesTheRow(t *testing.T) {
	r := &fakeReader{findings: []recall.ReviewFinding{{Key: "SC-1", File: "a.go", Slug: "x", Class: "tests"}}}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"a.go"}, "", 20, true, depsFor(r, "")))
	assert.Contains(t, out.String(), `"class": "tests"`)
}

func TestRunFindings_groupsByFile(t *testing.T) {
	r := &fakeReader{findings: []recall.ReviewFinding{
		{Key: "SC-1", File: "a.go", Slug: "one", Class: "tests"},
		{Key: "SC-2", File: "a.go", Slug: "two", Class: "docs"},
		{Key: "SC-3", File: "b.go", Slug: "three", Class: "design"},
	}}
	var out bytes.Buffer
	require.NoError(t, RunFindings(context.Background(), &out, []string{"a.go", "b.go"}, "", 20, false, depsFor(r, "")))

	var headings []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, " ") {
			headings = append(headings, line)
		}
	}
	assert.Equal(t, []string{"a.go", "b.go"}, headings, "one heading per file, not one per finding")
}

func TestRunFindings_storeOpenErrorSurfaces(t *testing.T) {
	deps := Deps{
		DBPath:   func() string { return "unused" },
		NewStore: func(string) (recall.FindingsReader, error) { return nil, errors.WithDetails("cannot open") },
		Project:  func(context.Context) string { return "" },
	}
	var out bytes.Buffer
	require.Error(t, RunFindings(context.Background(), &out, []string{"a.go"}, "", 20, false, deps))
}

func TestBuildReviewCmd_requiresAPath(t *testing.T) {
	cmd := BuildReviewCmd(DefaultDeps())
	findings, _, err := cmd.Find([]string{"findings"})
	require.NoError(t, err)
	assert.Equal(t, "findings", findings.Name())
	assert.Error(t, findings.Args(findings, nil), "a findings query with no path has nothing to answer about")
	require.NotNil(t, findings.Flags().Lookup("key"), "the daemon forwards --key; cobra must accept it")
	require.NotNil(t, findings.Flags().Lookup("limit"))
	require.NotNil(t, findings.Flags().Lookup("json"))
}

func TestDefaultDeps_projectComesFromTheRequestNotTheProcess(t *testing.T) {
	// Nothing was injected onto this context, so the default project answers.
	assert.Equal(t, "", DefaultDeps().Project(context.Background()))
	assert.NotNil(t, DefaultDeps().DBPath)
}

// The acceptance criterion end to end, over a real store rather than a fake:
// a round is recorded the way the review loop records it, the fixer's
// disposition is attached, and the command answers for the file — asked with
// the raw `<File>.go:<line>` shape an agent actually has in hand.
func TestRunFindings_realStoreAnswersAfterALoopCloses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recall.db")
	writer, err := recall.NewSQLiteStore(dbPath)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, writer.RecordReviewFindings(ctx, []recall.ReviewFinding{{
		// The writer's own normalization: lower-cased, no `:<line>`.
		Key: "SC-5123", PR: 512, Round: 2,
		File: "internal/daemon/pr_review_loop.go", Slug: "vocabulary not updated", Class: "dependents",
	}}))
	require.NoError(t, writer.SetFindingDisposition(ctx, "", "SC-5123", 512, 2, "done", "updated the switch"))
	require.NoError(t, writer.Close())

	// One store per run, because RunFindings owns and closes what NewStore
	// hands it — exactly as each CLI invocation does.
	deps := Deps{
		DBPath: func() string { return dbPath },
		NewStore: func(path string) (recall.FindingsReader, error) {
			return recall.NewSQLiteStore(path)
		},
		Project: func(context.Context) string { return "" },
	}
	var out bytes.Buffer
	require.NoError(t, RunFindings(ctx, &out, []string{"Internal/Daemon/PR_Review_Loop.go:42"}, "SC-5398", 20, false, deps))

	got := out.String()
	assert.Contains(t, got, "[dependents]")
	assert.Contains(t, got, "SC-5123")
	assert.Contains(t, got, "round 2")
	assert.Contains(t, got, "done")
	assert.Contains(t, got, "note: updated the switch")

	// And a file nothing was ever found in answers, rather than failing.
	out.Reset()
	require.NoError(t, RunFindings(ctx, &out, []string{"internal/daemon/untouched.go"}, "SC-5398", 20, false, deps))
	assert.Equal(t, "No prior findings recorded for these files.\n", out.String())
}
