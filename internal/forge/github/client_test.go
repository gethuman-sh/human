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
