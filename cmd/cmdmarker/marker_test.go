package cmdmarker

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/tracker"
)

// stubProvider implements tracker.Provider; only the comment methods carry
// behavior, the rest satisfy the interface.
type stubProvider struct {
	comments   []tracker.Comment
	added      []string
	listErr    error
	addErr     error
	addedKeys  []string
	listedKeys []string
}

func (s *stubProvider) ListIssues(context.Context, tracker.ListOptions) ([]tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) GetIssue(context.Context, string) (*tracker.Issue, error) { return nil, nil }
func (s *stubProvider) CreateIssue(_ context.Context, issue *tracker.Issue) (*tracker.Issue, error) {
	return issue, nil
}
func (s *stubProvider) ListComments(_ context.Context, key string) ([]tracker.Comment, error) {
	s.listedKeys = append(s.listedKeys, key)
	return s.comments, s.listErr
}
func (s *stubProvider) AddComment(_ context.Context, key, body string) (*tracker.Comment, error) {
	s.addedKeys = append(s.addedKeys, key)
	s.added = append(s.added, body)
	if s.addErr != nil {
		return nil, s.addErr
	}
	return &tracker.Comment{Body: body}, nil
}
func (s *stubProvider) LinkIssues(context.Context, string, string) error      { return nil }
func (s *stubProvider) DeleteIssue(context.Context, string) error             { return nil }
func (s *stubProvider) TransitionIssue(context.Context, string, string) error { return nil }
func (s *stubProvider) AssignIssue(context.Context, string, string) error     { return nil }
func (s *stubProvider) GetCurrentUser(context.Context) (string, error)        { return "", nil }
func (s *stubProvider) EditIssue(context.Context, string, tracker.EditOptions) (*tracker.Issue, error) {
	return nil, nil
}
func (s *stubProvider) ListStatuses(context.Context, string) ([]tracker.Status, error) {
	return nil, nil
}

func TestRunMarkerPost_rendersAndPosts(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunMarkerPost(context.Background(), p, &buf, "SC-1", "ready-for-review", "",
		[]string{"engineering=HUM-89", "branch=main", "commits=abc, def"}, "")
	require.NoError(t, err)

	require.Len(t, p.added, 1)
	assert.Equal(t, "[human:ready-for-review]\nengineering: HUM-89\nbranch: main\ncommits: abc, def", p.added[0])
	assert.Equal(t, []string{"SC-1"}, p.addedKeys)
	assert.Contains(t, buf.String(), "[human:ready-for-review]")
}

func TestRunMarkerPost_validationBlocksBadMarker(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunMarkerPost(context.Background(), p, &buf, "SC-1", "ready-for-review", "", []string{"branch=main"}, "")
	require.Error(t, err)
	assert.Empty(t, p.added, "invalid marker must not be posted")
}

func TestRunMarkerPost_headToken(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer

	err := RunMarkerPost(context.Background(), p, &buf, "SC-1", "bug-verify", "NOT DONE", nil, "gap: no regression test")
	require.NoError(t, err)
	require.Len(t, p.added, 1)
	assert.Equal(t, "[human:bug-verify] NOT DONE\n\ngap: no regression test", p.added[0])
}

func TestRunMarkerPost_badFieldSyntax(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer
	err := RunMarkerPost(context.Background(), p, &buf, "SC-1", "plan-ready", "", []string{"noequals"}, "")
	require.Error(t, err)
}

func TestRunMarkerPost_addCommentError(t *testing.T) {
	p := &stubProvider{addErr: errors.WithDetails("tracker down")}
	var buf bytes.Buffer
	err := RunMarkerPost(context.Background(), p, &buf, "SC-1", "review-started", "", nil, "")
	require.Error(t, err)
}

func TestRunMarkerShow_JSON(t *testing.T) {
	now := time.Now()
	p := &stubProvider{comments: []tracker.Comment{
		{Body: "[human:deployed]\npr: https://x/1", Created: now.Add(-time.Hour)},
		{Body: "[human:deployed]\npr: https://x/2", Created: now},
	}}
	var buf bytes.Buffer

	err := RunMarkerShow(context.Background(), p, &buf, "SC-1", "deployed", false)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"type": "deployed"`)
	assert.Contains(t, buf.String(), "https://x/2")
	assert.NotContains(t, buf.String(), "https://x/1", "latest wins")
}

func TestRunMarkerShow_raw(t *testing.T) {
	p := &stubProvider{comments: []tracker.Comment{{Body: "[human:review-started]", Created: time.Now()}}}
	var buf bytes.Buffer

	err := RunMarkerShow(context.Background(), p, &buf, "SC-1", "review-started", true)
	require.NoError(t, err)
	assert.Equal(t, "[human:review-started]\n", buf.String())
}

func TestRunMarkerShow_missing(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer
	err := RunMarkerShow(context.Background(), p, &buf, "SC-1", "deployed", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such marker")
}

func TestRunMarkerShow_listError(t *testing.T) {
	p := &stubProvider{listErr: errors.WithDetails("tracker down")}
	var buf bytes.Buffer
	err := RunMarkerShow(context.Background(), p, &buf, "SC-1", "deployed", false)
	require.Error(t, err)
}

func TestRunMarkerList_newestFirstJSON(t *testing.T) {
	now := time.Now()
	p := &stubProvider{comments: []tracker.Comment{
		{Body: "[human:review-started]", Created: now.Add(-time.Hour)},
		{Body: "plain comment", Created: now},
		{Body: "[human:deployed]\npr: https://x/1", Created: now},
	}}
	var buf bytes.Buffer

	err := RunMarkerList(context.Background(), p, &buf, "SC-1")
	require.NoError(t, err)
	out := buf.String()
	assert.Less(t, strings.Index(out, "deployed"), strings.Index(out, "review-started"))
}

func TestRunMarkerList_emptyIsArray(t *testing.T) {
	p := &stubProvider{}
	var buf bytes.Buffer
	require.NoError(t, RunMarkerList(context.Background(), p, &buf, "SC-1"))
	assert.Equal(t, "[]\n", buf.String())
}

func TestParseFieldArgs_orderAndDuplicates(t *testing.T) {
	fields, order, err := parseFieldArgs([]string{"b=2", "a=1", "b=3"})
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "a"}, order)
	assert.Equal(t, map[string]string{"a": "1", "b": "3"}, fields)
}

func TestResolveBody_sources(t *testing.T) {
	body, err := resolveBody("inline", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "inline", body)

	body, err = resolveBody("", "-", strings.NewReader("from stdin"))
	require.NoError(t, err)
	assert.Equal(t, "from stdin", body)

	_, err = resolveBody("x", "y", nil)
	require.Error(t, err)
}

func TestBuildMarkerCmd_Subcommands(t *testing.T) {
	cmd := BuildMarkerCmd(cmdutil.DefaultDeps())
	names := make([]string, 0)
	for _, sub := range cmd.Commands() {
		names = append(names, strings.Fields(sub.Use)[0])
	}
	assert.ElementsMatch(t, []string{"post", "show", "list"}, names)
}
