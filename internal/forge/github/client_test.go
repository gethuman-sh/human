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
