package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/recall"
	"github.com/gethuman-sh/human/internal/tracker"
)

func newFeedbackStore(t *testing.T) *recall.SQLiteStore {
	t.Helper()
	s, err := recall.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// countingRunner records every prompt it is given and answers with a fixed
// block, so a test can assert both what the model saw and how often.
type countingRunner struct {
	prompts []string
	answer  string
	err     error
}

func (r *countingRunner) Run(_ context.Context, prompt string) (string, error) {
	r.prompts = append(r.prompts, prompt)
	return r.answer, r.err
}

func seedFinding(t *testing.T, s *recall.SQLiteStore, project, key, file, class, text string) {
	t.Helper()
	require.NoError(t, s.RecordReviewFindings(context.Background(), []recall.ReviewFinding{
		{Project: project, Key: key, PR: 1, Round: 1, File: file, Slug: file + " " + class, Class: class, Text: text},
	}))
}

func TestFeedbackAdvice_nilDepsAndEmptyRecordYieldNothing(t *testing.T) {
	var none *FeedbackDeps
	assert.Empty(t, none.Advice(context.Background(), "SC-1", BoardPlanning, ""))

	runner := &countingRunner{answer: "- something"}
	f := &FeedbackDeps{Record: newFeedbackStore(t), Runner: runner}
	assert.Empty(t, f.Advice(context.Background(), "SC-1", BoardPlanning, ""))
	assert.Empty(t, runner.prompts, "an empty record makes no model call")
}

func TestFeedbackAdvice_rowsReachTheModelAndTheBlockComesBack(t *testing.T) {
	store := newFeedbackStore(t)
	seedFinding(t, store, "p", "SC-9", "internal/x/y.go", "contract", "the wire field was added without a bump")
	require.NoError(t, store.SetFindingDisposition(context.Background(), "p", "SC-9", 1, 1, "done", "bumped it"))
	seedFinding(t, store, "other", "SC-2", "internal/x/y.go", "docs", "belongs to another project")
	runner := &countingRunner{answer: "```\n- internal/x/y.go [contract]: bump the protocol\n```"}
	f := &FeedbackDeps{
		Record: store, Runner: runner, Project: "p",
		Ticket: &fakeGetter{issue: &tracker.Issue{Key: "SC-1", Title: "Add a field", Description: "touches internal/x/y.go"}},
	}

	block := f.Advice(context.Background(), "SC-1", BoardPlanning, "")

	assert.Equal(t, "- internal/x/y.go [contract]: bump the protocol", block, "fences and whitespace are stripped")
	require.Len(t, runner.prompts, 1)
	prompt := runner.prompts[0]
	assert.Contains(t, prompt, `stage "planning" on ticket SC-1 ("Add a field")`)
	assert.Contains(t, prompt, "internal/x/y.go [contract] SC-9 round 1, fixer: done — the wire field was added without a bump (fixer: bumped it)")
	assert.NotContains(t, prompt, "another project", "projects do not share findings")
	assert.Contains(t, prompt, "- contract: 1")
}

func TestFeedbackAdvice_cacheHoldsUntilTheRecordGrows(t *testing.T) {
	store := newFeedbackStore(t)
	seedFinding(t, store, "", "SC-9", "a.go", "tests", "untested")
	runner := &countingRunner{answer: "- a.go [tests]: add the test"}
	f := &FeedbackDeps{Record: store, Runner: runner, Cache: &FeedbackCache{}}

	first := f.Advice(context.Background(), "SC-1", BoardImplementation, "")
	second := f.Advice(context.Background(), "SC-1", BoardImplementation, "")
	assert.Equal(t, first, second)
	assert.Len(t, runner.prompts, 1, "a relaunch with no new finding pays nothing")

	f.Advice(context.Background(), "SC-1", BoardVerification, "")
	assert.Len(t, runner.prompts, 2, "another stage is another briefing")

	seedFinding(t, store, "", "SC-10", "b.go", "docs", "stale doc")
	f.Advice(context.Background(), "SC-1", BoardImplementation, "")
	assert.Len(t, runner.prompts, 3, "a new row invalidates the cached block")
}

func TestFeedbackAdvice_failuresLaunchWithoutAdvice(t *testing.T) {
	store := newFeedbackStore(t)
	seedFinding(t, store, "", "SC-9", "a.go", "tests", "untested")

	failing := &countingRunner{err: errors.New("model unreachable")}
	f := &FeedbackDeps{Record: store, Runner: failing}
	assert.Empty(t, f.Advice(context.Background(), "SC-1", BoardPlanning, ""))

	none := &countingRunner{answer: "NONE."}
	f = &FeedbackDeps{Record: store, Runner: none}
	assert.Empty(t, f.Advice(context.Background(), "SC-1", BoardPlanning, ""), "the sentinel is no block")

	slow := FeedbackRunnerFunc(func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	f = &FeedbackDeps{Record: store, Runner: slow, Timeout: 10 * time.Millisecond}
	assert.Empty(t, f.Advice(context.Background(), "SC-1", BoardPlanning, ""), "a timeout is a missing block, never a stalled launch")
}

func TestWithFeedback_keepsTheDispatchLineFirst(t *testing.T) {
	assert.Equal(t, "/human-execute SC-1", withFeedback("/human-execute SC-1", ""))
	got := withFeedback("/human-execute SC-1\n", "- a.go [tests]: add the test")
	lines := strings.Split(got, "\n")
	assert.Equal(t, "/human-execute SC-1", lines[0])
	assert.Equal(t, "", lines[1])
	assert.Equal(t, FeedbackHeading, lines[2])
	assert.Contains(t, got, "- a.go [tests]: add the test\n")
}

func TestFeedbackPrompt_rowsAreBoundedAndDeduplicated(t *testing.T) {
	long := strings.Repeat("x", 400)
	rows := appendUnseenFindings(
		[]recall.ReviewFinding{{Key: "SC-1", File: "a.go", Slug: "s", PR: 1, Round: 1, Text: long}},
		[]recall.ReviewFinding{
			{Key: "SC-1", File: "a.go", Slug: "s", PR: 1, Round: 1, Text: long},
			{Key: "SC-2", File: "b.go", Slug: "t", PR: 2, Round: 1},
		})
	require.Len(t, rows, 2, "the scoped row is not listed twice")
	prompt := feedbackPrompt("SC-3", "", BoardPlanning, nil, rows, map[string]int{"": 2, "docs": 5})
	assert.Contains(t, prompt, strings.Repeat("x", 300)+"…")
	assert.NotContains(t, prompt, strings.Repeat("x", 301))
	assert.Contains(t, prompt, "- docs: 5\n- (unclassified): 2")
	assert.Contains(t, prompt, "b.go [unclassified] SC-2 round 1, fixer: no fixer report yet")
}

func TestFeedbackScope_planningReadsTheTicketImplementationReadsThePlan(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "internal", "x"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "internal", "x", "y.go"), []byte("package x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "README.md"), []byte("#"), 0o600))
	ticket := &fakeGetter{issue: &tracker.Issue{Title: "Fix `internal/x/y.go` and README.md", Description: "not internal/x/missing.go, nor v1.2.3, nor example.com"}}
	plan := &fakeCommenter{comments: []tracker.Comment{
		{ID: "1", Body: PlanCommentHeader + "\n\nStep 1: change README.md only.", Created: time.Now()},
	}}
	f := &FeedbackDeps{Workspace: ws, Ticket: ticket, Comments: plan}

	assert.Equal(t, []string{"internal/x/y.go", "README.md"}, f.scope(context.Background(), "SC-1", BoardPlanning, "").files)
	assert.Equal(t, []string{"README.md"}, f.scope(context.Background(), "SC-1", BoardImplementation, "").files)

	noPlan := &FeedbackDeps{Workspace: ws, Ticket: ticket, Comments: &fakeCommenter{}}
	assert.Equal(t, []string{"internal/x/y.go", "README.md"}, noPlan.scope(context.Background(), "SC-1", BoardImplementation, "").files,
		"no plan falls back to the ticket text")
	assert.Equal(t, "Fix `internal/x/y.go` and README.md", noPlan.scope(context.Background(), "SC-1", BoardImplementation, "").title)
}

func TestFeedbackScope_prStagesUseTheBranchDiff(t *testing.T) {
	var asked string
	f := &FeedbackDeps{Workspace: "/ws", ChangedFiles: func(_ context.Context, ws, branch string) ([]string, error) {
		asked = ws + " " + branch
		return []string{"b.go", "a.go"}, nil
	}}
	got := f.scope(context.Background(), "SC-1", prFixAgentStage, "autofix/sc-1")
	assert.Equal(t, []string{"b.go", "a.go"}, got.files)
	assert.Equal(t, "/ws autofix/sc-1", asked)

	assert.Empty(t, f.scope(context.Background(), "SC-1", prReviewAgentStage, "").files, "no branch, no diff")
	failing := &FeedbackDeps{Workspace: "/ws", ChangedFiles: func(context.Context, string, string) ([]string, error) {
		return nil, errors.New("no such ref")
	}}
	assert.Empty(t, failing.scope(context.Background(), "SC-1", deployFixAgentStage, "x").files, "an unreadable diff is an empty scope, not an error")
}

func TestPathTokens_rejectsEscapesAndDirectories(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "dir"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "dir", "f.go"), []byte(""), 0o600))
	assert.Equal(t, []string{"dir/f.go"}, pathTokens("dir, dir/f.go, ../dir/f.go, ./dir/f.go, dir/f.go.", ws))
	assert.Nil(t, pathTokens("dir/f.go", ""))
}

func TestGitChangedFiles_listsTheBranchAgainstTheRemoteDefault(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run(origin, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base"), 0o600))
	run(origin, "add", "base.txt")
	run(origin, "commit", "-q", "-m", "base")
	run(origin, "checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(origin, "changed.txt"), []byte("new"), 0o600))
	run(origin, "add", "changed.txt")
	run(origin, "commit", "-q", "-m", "feature")
	run(origin, "checkout", "-q", "main")

	ws := filepath.Join(t.TempDir(), "ws")
	run(filepath.Dir(ws), "clone", "-q", origin, ws)

	files, err := gitChangedFiles(ctx, ws, "feature")
	require.NoError(t, err)
	assert.Equal(t, []string{"changed.txt"}, files)

	_, err = gitChangedFiles(ctx, ws, "no-such-branch")
	assert.Error(t, err)
}

func TestLaunchAgent_appendsTheAdviceAfterTheDispatchLine(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	var asked []string
	deps.Feedback = func(_ context.Context, pmKey string, stage BoardStage, branch string) string {
		asked = append(asked, pmKey+" "+string(stage)+" "+branch)
		return "- a.go [tests]: add the test"
	}

	launched, err := deps.launchAgent(context.Background(), "SC-1", agentNameFor("SC-1", prFixAgentStage), prFixDispatch("SC-1", 7, "autofix/sc-1"))
	require.NoError(t, err)
	assert.True(t, launched)
	assert.Equal(t, []string{"SC-1 prfix autofix/sc-1"}, asked, "the branch comes off the dispatch line")
	lines := strings.Split(l.prompt, "\n")
	assert.Equal(t, prFixDispatch("SC-1", 7, "autofix/sc-1"), lines[0], "skills parse their arguments off an unchanged first line")
	assert.Contains(t, l.prompt, FeedbackHeading+"\n\n- a.go [tests]: add the test")
}

func TestLaunchAgent_noAdviceLeavesThePromptUntouched(t *testing.T) {
	c := &fakeCommenter{}
	l := &fakeLauncher{}
	deps := newDeps(c, l, &fakeDeployer{})
	_, err := deps.launchAgent(context.Background(), "SC-1", agentNameFor("SC-1", BoardImplementation), "/human-execute SC-1")
	require.NoError(t, err)
	assert.Equal(t, "/human-execute SC-1", l.prompt, "nil Feedback: byte-identical")

	deps.Feedback = func(context.Context, string, BoardStage, string) string { return "" }
	_, err = deps.launchAgent(context.Background(), "SC-1", agentNameFor("SC-1", BoardImplementation), "/human-execute SC-1")
	require.NoError(t, err)
	assert.Equal(t, "/human-execute SC-1", l.prompt, "an empty block: byte-identical")
}

func TestDispatchBranch(t *testing.T) {
	assert.Equal(t, "feat/x", dispatchBranch("/human-pr-review SC-1 --pr=3 --branch=feat/x\n\nmore"))
	assert.Equal(t, "", dispatchBranch("/human-execute SC-1"))
	assert.Equal(t, "", dispatchBranch("/human-execute SC-1\n--branch=not-on-the-dispatch-line"))
}
