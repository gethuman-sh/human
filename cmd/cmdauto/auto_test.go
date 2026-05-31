package cmdauto

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/cmd/cmdutil"
	"github.com/gethuman-sh/human/internal/github"
	"github.com/gethuman-sh/human/internal/gitrepo"
	"github.com/gethuman-sh/human/internal/tracker"
)

func TestBuildAutoPRCreateCmd_structure(t *testing.T) {
	prCmd := BuildAutoPRCreateCmd(cmdutil.Deps{})
	assert.Equal(t, "pr", prCmd.Name())

	create := prCmd.Commands()
	require.Len(t, create, 1)
	assert.Equal(t, "create", create[0].Name())

	flags := create[0].Flags()
	for _, name := range []string{"repo", "head", "base", "title", "body"} {
		assert.NotNil(t, flags.Lookup(name), "expected --%s flag", name)
	}
}

func TestBuildAutoPRCreateCmd_run(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/gethuman-sh/human/pulls", r.URL.Path)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"title":"Fix","html_url":"https://github.com/gethuman-sh/human/pull/7"}`))
	}))
	defer srv.Close()

	// Derive the forge from a github origin remote without touching a real repo.
	prev := gitrepo.OriginURL
	gitrepo.OriginURL = func(_ context.Context, _ string) (string, error) {
		return "git@github.com:gethuman-sh/human.git", nil
	}
	defer func() { gitrepo.OriginURL = prev }()

	deps := cmdutil.Deps{
		LoadInstances: func(_ string) ([]tracker.Instance, error) {
			return []tracker.Instance{{
				Name:     "gh",
				Kind:     "github",
				URL:      srv.URL,
				Provider: github.New(srv.URL, "t"),
			}}, nil
		},
		InstanceFromFlags: func(_ *cobra.Command) *tracker.Instance { return nil },
		AuditLogPath:      func() string { return "" },
	}

	root := &cobra.Command{Use: "human"}
	root.AddCommand(BuildAutoPRCreateCmd(deps))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"pr", "create", "--head", "fix-login", "--title", "Fix"})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "https://github.com/gethuman-sh/human/pull/7")
}
