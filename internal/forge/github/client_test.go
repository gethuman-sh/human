package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gethuman-sh/human/internal/forge"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreatePullRequest_happy(t *testing.T) {
	var gotBody pullCreateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/pulls", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"number":42,"title":"Fix login","html_url":"https://github.com/octocat/hello-world/pull/42"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	pr, err := client.CreatePullRequest(context.Background(), &forge.PullRequest{
		Repo:  "octocat/hello-world",
		Base:  "main",
		Head:  "autofix/hum-105",
		Title: "Fix login",
		Body:  "Closes octocat/hello-world#7",
	})

	require.NoError(t, err)
	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "https://github.com/octocat/hello-world/pull/42", pr.URL)
	assert.Equal(t, "Fix login", pr.Title)

	assert.Equal(t, "Fix login", gotBody.Title)
	assert.Equal(t, "autofix/hum-105", gotBody.Head)
	assert.Equal(t, "main", gotBody.Base)
	assert.Equal(t, "Closes octocat/hello-world#7", gotBody.Body)
}

func TestCreatePullRequest_invalidRepo(t *testing.T) {
	client := New("https://api.github.com", "ghp_test")
	_, err := client.CreatePullRequest(context.Background(), &forge.PullRequest{
		Repo:  "no-slash",
		Base:  "main",
		Head:  "feature",
		Title: "x",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestCreatePullRequest_draft(t *testing.T) {
	var gotBody pullCreateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"number":7,"title":"WIP","html_url":"https://github.com/octocat/hello-world/pull/7"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	pr, err := client.CreatePullRequest(context.Background(), &forge.PullRequest{
		Repo:  "octocat/hello-world",
		Base:  "main",
		Head:  "autofix/1387",
		Title: "WIP",
		Draft: true,
	})
	require.NoError(t, err)
	assert.True(t, gotBody.Draft, "create payload must set draft:true")
	assert.True(t, pr.Draft, "returned PR echoes the draft flag")
}

func TestMarkReadyForReview_happy(t *testing.T) {
	var graphqlBody graphQLRequest
	var sawGet bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octocat/hello-world/pulls/42":
			sawGet = true
			_, _ = fmt.Fprint(w, `{"node_id":"PR_node_abc","head":{"sha":"deadbeef"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &graphqlBody))
			_, _ = fmt.Fprint(w, `{"data":{"markPullRequestReadyForReview":{"pullRequest":{"isDraft":false}}}}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.MarkReadyForReview(context.Background(), "octocat/hello-world", 42)
	require.NoError(t, err)
	assert.True(t, sawGet, "must resolve the PR node id via REST first")
	assert.Contains(t, graphqlBody.Query, "markPullRequestReadyForReview")
	assert.Equal(t, "PR_node_abc", graphqlBody.Variables["id"])
}

func TestMarkReadyForReview_graphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprint(w, `{"node_id":"PR_1"}`)
			return
		}
		// GitHub returns HTTP 200 with an errors array on a GraphQL-level failure.
		_, _ = fmt.Fprint(w, `{"errors":[{"message":"Pull request is not a draft"}]}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.MarkReadyForReview(context.Background(), "octocat/hello-world", 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a draft")
}

func TestMarkReadyForReview_noNodeID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"head":{"sha":"x"}}`) // response carries no node_id
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.MarkReadyForReview(context.Background(), "octocat/hello-world", 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node id")
}

func TestFindOpenPullRequest_found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/pulls", r.URL.Path)
		assert.Equal(t, "octocat:autofix/989", r.URL.Query().Get("head"))
		assert.Equal(t, "open", r.URL.Query().Get("state"))

		_, _ = fmt.Fprint(w, `[{"number":42,"title":"Fix login","html_url":"https://github.com/octocat/hello-world/pull/42","state":"open","head":{"ref":"autofix/989"}}]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	pr, err := client.FindOpenPullRequest(context.Background(), "octocat/hello-world", "autofix/989")

	require.NoError(t, err)
	require.NotNil(t, pr)
	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "https://github.com/octocat/hello-world/pull/42", pr.URL)
	assert.Equal(t, "Fix login", pr.Title)
}

func TestFindOpenPullRequest_none(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	pr, err := client.FindOpenPullRequest(context.Background(), "octocat/hello-world", "autofix/989")

	require.NoError(t, err)
	assert.Nil(t, pr)
}

func TestFindOpenPullRequest_invalidRepo(t *testing.T) {
	client := New("https://api.github.com", "ghp_test")
	_, err := client.FindOpenPullRequest(context.Background(), "no-slash", "autofix/989")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

// checksServer serves the three endpoints PullRequestChecks touches with
// canned check-run and combined-status payloads.
func checksServer(t *testing.T, checkRuns, combined string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls/7":
			_, _ = fmt.Fprint(w, `{"head":{"sha":"abc123"}}`)
		case "/repos/octocat/hello-world/commits/abc123/check-runs":
			_, _ = fmt.Fprint(w, checkRuns)
		case "/repos/octocat/hello-world/commits/abc123/status":
			_, _ = fmt.Fprint(w, combined)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestPullRequestChecks_verdicts(t *testing.T) {
	cases := []struct {
		name      string
		checkRuns string
		combined  string
		want      forge.ChecksState
	}{
		{"all green", `{"check_runs":[{"status":"completed","conclusion":"success"}]}`,
			`{"state":"success","total_count":1}`, forge.ChecksPassing},
		{"run failed", `{"check_runs":[{"status":"completed","conclusion":"failure"}]}`,
			`{"state":"success","total_count":0}`, forge.ChecksFailing},
		{"run still running", `{"check_runs":[{"status":"in_progress","conclusion":""}]}`,
			`{"state":"success","total_count":0}`, forge.ChecksPending},
		{"legacy status failed", `{"check_runs":[]}`,
			`{"state":"failure","total_count":2}`, forge.ChecksFailing},
		{"legacy status pending", `{"check_runs":[]}`,
			`{"state":"pending","total_count":2}`, forge.ChecksPending},
		// GitHub reports "pending" with zero statuses when only check runs
		// exist — no signal, must not hold the gate.
		{"no CI at all", `{"check_runs":[]}`,
			`{"state":"pending","total_count":0}`, forge.ChecksPassing},
		{"skipped and neutral pass", `{"check_runs":[{"status":"completed","conclusion":"skipped"},{"status":"completed","conclusion":"neutral"}]}`,
			`{"state":"pending","total_count":0}`, forge.ChecksPassing},
		// SC-2602: a superseded build is called off, not failed. A cancelled run with
		// no conclusive sibling is non-conclusive — the gate must keep waiting, never
		// declare a failure.
		{"cancelled superseded, sibling passed", `{"check_runs":[{"name":"unit","status":"completed","conclusion":"success","started_at":"2026-08-01T10:00:00Z"},{"name":"build","status":"completed","conclusion":"cancelled","started_at":"2026-08-01T10:00:00Z"}]}`,
			`{"state":"success","total_count":0}`, forge.ChecksPending},
		// SC-2602: a cancelled attempt must not overrule the re-run that replaced it —
		// judge the latest run per check name, so a passed re-run settles the gate green.
		{"cancelled then passing rerun same name", `{"check_runs":[{"name":"build","status":"completed","conclusion":"cancelled","started_at":"2026-08-01T10:00:00Z"},{"name":"build","status":"completed","conclusion":"success","started_at":"2026-08-01T10:05:00Z"}]}`,
			`{"state":"success","total_count":0}`, forge.ChecksPassing},
		// SC-2602: a real failure among cancellations still fails, exactly as today.
		{"cancelled and a real failure", `{"check_runs":[{"name":"build","status":"completed","conclusion":"cancelled","started_at":"2026-08-01T10:00:00Z"},{"name":"unit","status":"completed","conclusion":"failure","started_at":"2026-08-01T10:00:00Z"}]}`,
			`{"state":"success","total_count":0}`, forge.ChecksFailing},
		// SC-2602: order-independence — a later cancelled attempt does not shadow an
		// earlier success of the same check.
		{"passing then cancelled rerun same name", `{"check_runs":[{"name":"build","status":"completed","conclusion":"success","started_at":"2026-08-01T10:00:00Z"},{"name":"build","status":"completed","conclusion":"cancelled","started_at":"2026-08-01T10:05:00Z"}]}`,
			`{"state":"success","total_count":0}`, forge.ChecksPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := checksServer(t, tc.checkRuns, tc.combined)
			defer srv.Close()
			client := New(srv.URL, "ghp_test")
			state, err := client.PullRequestChecks(context.Background(), "octocat/hello-world", 7)
			require.NoError(t, err)
			assert.Equal(t, tc.want, state)
		})
	}
}

func TestPullRequestMergeable_verdicts(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"mergeable true", `{"mergeable":true}`, true},
		{"mergeable false", `{"mergeable":false}`, false},
		// GitHub returns null while it computes the value asynchronously — treat
		// it as not mergeable so a caller never merges on an unknown state.
		{"mergeable null", `{"mergeable":null}`, false},
		{"mergeable absent", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/repos/octocat/hello-world/pulls/7", r.URL.Path)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			client := New(srv.URL, "ghp_test")
			mergeable, err := client.PullRequestMergeable(context.Background(), "octocat/hello-world", 7)
			require.NoError(t, err)
			assert.Equal(t, tc.want, mergeable)
		})
	}
}

func TestMergePullRequest_happy(t *testing.T) {
	var gotBody mergeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/repos/octocat/hello-world/pulls/7/merge", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &gotBody))
		_, _ = fmt.Fprint(w, `{"merged":true,"message":"Pull Request successfully merged"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	require.NoError(t, client.MergePullRequest(context.Background(), "octocat/hello-world", 7))
	assert.Equal(t, "merge", gotBody.MergeMethod)
}

func TestMergePullRequest_notMerged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"merged":false,"message":"Base branch was modified"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	err := client.MergePullRequest(context.Background(), "octocat/hello-world", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Base branch was modified")
}

func TestDeleteBranch_happy(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	require.NoError(t, client.DeleteBranch(context.Background(), "octocat/hello-world", "feat/x"))
	assert.Equal(t, "/repos/octocat/hello-world/git/refs/heads/feat%2Fx", gotPath)
}

func TestCreatePullRequest_httpError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	_, err := client.CreatePullRequest(context.Background(), &forge.PullRequest{
		Repo:  "octocat/hello-world",
		Base:  "main",
		Head:  "feature",
		Title: "Will fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned")
}

func TestPullRequestMerged_verdicts(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"merged true", `{"merged":true}`, true},
		{"merged false", `{"merged":false}`, false},
		{"merged absent", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/repos/octocat/hello-world/pulls/7", r.URL.Path)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			client := New(srv.URL, "ghp_test")
			merged, err := client.PullRequestMerged(context.Background(), "octocat/hello-world", 7)
			require.NoError(t, err)
			assert.Equal(t, tc.want, merged)
		})
	}
}

func TestFindOpenWork_openPRNamingKey_isFound(t *testing.T) {
	// The bug's false negative: a human's PR whose TITLE names the ticket key,
	// on a branch whose name does NOT match the pipeline convention. Text/recall
	// never saw it; the forge finder must.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls":
			assert.Equal(t, "open", r.URL.Query().Get("state"))
			_, _ = fmt.Fprint(w, `[{"number":57,"title":"[SC-2648] fix dragged card","html_url":"https://github.com/octocat/hello-world/pull/57","state":"open","head":{"ref":"stephan/card-fix"}}]`)
		case "/repos/octocat/hello-world/branches":
			_, _ = fmt.Fprint(w, `[]`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	work, err := client.FindOpenWork(context.Background(), "octocat/hello-world", "SC-2648")
	require.NoError(t, err)
	require.Len(t, work, 1)
	assert.Equal(t, "pull-request", work[0].Kind)
	assert.Equal(t, 57, work[0].Number)
	assert.Equal(t, "https://github.com/octocat/hello-world/pull/57", work[0].URL)
	assert.Equal(t, "[SC-2648] fix dragged card", work[0].Title)
}

func TestFindOpenWork_branchNamingKey_isFound(t *testing.T) {
	// A pushed branch with no PR yet (the implementation/review window) is still
	// "underway".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls":
			_, _ = fmt.Fprint(w, `[]`)
		case "/repos/octocat/hello-world/branches":
			_, _ = fmt.Fprint(w, `[{"name":"autofix/sc-2648"},{"name":"main"}]`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	work, err := client.FindOpenWork(context.Background(), "octocat/hello-world", "SC-2648")
	require.NoError(t, err)
	require.Len(t, work, 1)
	assert.Equal(t, "branch", work[0].Kind)
	assert.Equal(t, "autofix/sc-2648", work[0].Ref)
}

func TestFindOpenWork_shorterKeyIsNotAPrefixMatch(t *testing.T) {
	// Ticket keys are sequential, so every key is a prefix of its later
	// siblings. SC-264 must not be "found" in SC-2648's work — reporting it
	// would stop a run over a pull request belonging to another ticket, the
	// false positive this signal exists to remove.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls":
			_, _ = fmt.Fprint(w, `[{"number":57,"title":"[SC-2648] fix dragged card","html_url":"u","head":{"ref":"autofix/sc-2648"}}]`)
		case "/repos/octocat/hello-world/branches":
			_, _ = fmt.Fprint(w, `[{"name":"autofix/sc-2648"},{"name":"main"}]`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	work, err := client.FindOpenWork(context.Background(), "octocat/hello-world", "SC-264")
	require.NoError(t, err)
	assert.Empty(t, work)

	// The real key still matches, so the boundary rule did not cost the signal.
	work, err = client.FindOpenWork(context.Background(), "octocat/hello-world", "SC-2648")
	require.NoError(t, err)
	require.Len(t, work, 1)
}

func TestFindOpenWork_nothingOpen_returnsEmpty(t *testing.T) {
	// The bug's false positive: a stale ticket overlaps in wording but has NO
	// open PR or branch. The forge finder reports nothing, so preflight must not
	// block. Encodes "merely overlapping wording does not stop anything".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls":
			_, _ = fmt.Fprint(w, `[{"number":9,"title":"[SC-1111] unrelated","html_url":"https://github.com/octocat/hello-world/pull/9","state":"open","head":{"ref":"autofix/sc-1111"}}]`)
		case "/repos/octocat/hello-world/branches":
			_, _ = fmt.Fprint(w, `[{"name":"main"},{"name":"autofix/sc-1111"}]`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	work, err := client.FindOpenWork(context.Background(), "octocat/hello-world", "SC-2648")
	require.NoError(t, err)
	assert.Empty(t, work)
}

func TestFindOpenWork_branchWithPR_notDoubleReported(t *testing.T) {
	// A branch that is already the head of a matched PR must be reported once (as
	// the PR), not also as a bare branch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls":
			_, _ = fmt.Fprint(w, `[{"number":57,"title":"[SC-2648] fix","html_url":"https://github.com/octocat/hello-world/pull/57","state":"open","head":{"ref":"autofix/sc-2648"}}]`)
		case "/repos/octocat/hello-world/branches":
			_, _ = fmt.Fprint(w, `[{"name":"autofix/sc-2648"}]`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, "ghp_test")
	work, err := client.FindOpenWork(context.Background(), "octocat/hello-world", "SC-2648")
	require.NoError(t, err)
	require.Len(t, work, 1)
	assert.Equal(t, "pull-request", work[0].Kind)
}

func TestFindOpenWork_invalidRepo(t *testing.T) {
	client := New("https://api.github.com", "ghp_test")
	_, err := client.FindOpenWork(context.Background(), "no-slash", "SC-2648")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

// readPRServer serves the three endpoints ReadPullRequest touches: the pull GET
// (identity + mergeability), the check-runs list, and the combined status.
func readPRServer(t *testing.T, pull, checkRuns, combined string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octocat/hello-world/pulls/7":
			_, _ = fmt.Fprint(w, pull)
		case "/repos/octocat/hello-world/commits/abc123/check-runs":
			_, _ = fmt.Fprint(w, checkRuns)
		case "/repos/octocat/hello-world/commits/abc123/status":
			_, _ = fmt.Fprint(w, combined)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestReadPullRequest_fullState(t *testing.T) {
	pull := `{"number":7,"head":{"ref":"feat","sha":"abc123"},"base":{"ref":"main"},"mergeable":true}`
	checkRuns := `{"check_runs":[` +
		`{"name":"build","status":"completed","conclusion":"success","started_at":"2026-08-01T10:00:00Z"},` +
		`{"name":"lint","status":"completed","conclusion":"failure","started_at":"2026-08-01T10:00:00Z","details_url":"https://ci/lint"}` +
		`]}`
	combined := `{"state":"failure","total_count":1,"statuses":[{"context":"ci/legacy","state":"failure","target_url":"https://ci/legacy"}]}`

	srv := readPRServer(t, pull, checkRuns, combined)
	defer srv.Close()
	client := New(srv.URL, "ghp_test")

	state, err := client.ReadPullRequest(context.Background(), "octocat/hello-world", 7)
	require.NoError(t, err)
	assert.Equal(t, 7, state.Number)
	assert.Equal(t, "feat", state.HeadRef)
	assert.Equal(t, "main", state.BaseRef)
	assert.Equal(t, "abc123", state.HeadSHA)
	assert.True(t, state.Mergeable)

	require.Len(t, state.Checks, 3)
	assert.Equal(t, forge.CheckResult{Name: "build", Conclusion: forge.ChecksPassing}, state.Checks[0])
	assert.Equal(t, forge.CheckResult{Name: "lint", Conclusion: forge.ChecksFailing, DetailsURL: "https://ci/lint"}, state.Checks[1])
	assert.Equal(t, forge.CheckResult{Name: "ci/legacy", Conclusion: forge.ChecksFailing, DetailsURL: "https://ci/legacy"}, state.Checks[2])
}

func TestReadPullRequest_mergeableNull(t *testing.T) {
	pull := `{"number":7,"head":{"ref":"feat","sha":"abc123"},"base":{"ref":"main"},"mergeable":null}`
	srv := readPRServer(t, pull, `{"check_runs":[]}`, `{"state":"success","total_count":0}`)
	defer srv.Close()
	client := New(srv.URL, "ghp_test")

	state, err := client.ReadPullRequest(context.Background(), "octocat/hello-world", 7)
	require.NoError(t, err)
	assert.False(t, state.Mergeable)
}

func TestReadPullRequest_dedupsRerun(t *testing.T) {
	// SC-2602: a cancelled attempt superseded by a passing re-run of the same
	// name collapses to one entry carrying the latest verdict.
	pull := `{"number":7,"head":{"ref":"feat","sha":"abc123"},"base":{"ref":"main"},"mergeable":true}`
	checkRuns := `{"check_runs":[` +
		`{"name":"build","status":"completed","conclusion":"cancelled","started_at":"2026-08-01T10:00:00Z"},` +
		`{"name":"build","status":"completed","conclusion":"success","started_at":"2026-08-01T10:05:00Z"}` +
		`]}`
	srv := readPRServer(t, pull, checkRuns, `{"state":"success","total_count":0}`)
	defer srv.Close()
	client := New(srv.URL, "ghp_test")

	state, err := client.ReadPullRequest(context.Background(), "octocat/hello-world", 7)
	require.NoError(t, err)
	require.Len(t, state.Checks, 1)
	assert.Equal(t, "build", state.Checks[0].Name)
	assert.Equal(t, forge.ChecksPassing, state.Checks[0].Conclusion)
}

func TestReadPullRequest_noHeadSHA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"number":7,"head":{}}`)
	}))
	defer srv.Close()
	client := New(srv.URL, "ghp_test")

	_, err := client.ReadPullRequest(context.Background(), "octocat/hello-world", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no head SHA")
}

func TestReadPullRequest_invalidRepo(t *testing.T) {
	client := New("https://api.github.com", "ghp_test")
	_, err := client.ReadPullRequest(context.Background(), "no-slash", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}
