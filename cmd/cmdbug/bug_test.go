package cmdbug

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/daemon"
)

// startTestDaemon starts an in-process daemon.Server wired with the given
// bug/security creators and returns its address.
func startTestDaemon(t *testing.T, token string,
	bug func(daemon.BugCreateRequest) (daemon.BugCreateResponse, error),
	sec func(daemon.SecurityCreateRequest) (daemon.SecurityCreateResponse, error),
) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv := &daemon.Server{
		Addr:            addr,
		Token:           token,
		CmdFactory:      func() *cobra.Command { return &cobra.Command{} }, // never invoked by these routes
		Logger:          zerolog.Nop(),
		BugCreator:      bug,
		SecurityCreator: sec,
	}
	go func() { _ = srv.ListenAndServe(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr
}

// pointEnvAtDaemon makes resolveDaemon find the given test server and also
// isolates the info-file lookup so an ambient ~/.human/daemon.json cannot leak
// in during the test.
func pointEnvAtDaemon(t *testing.T, addr, token string) {
	t.Helper()
	t.Setenv("HUMAN_DAEMON_ADDR", addr)
	t.Setenv("HUMAN_DAEMON_TOKEN", token)
}

func TestBuildBugCmd_structure(t *testing.T) {
	cmd := BuildBugCmd()
	assert.Equal(t, "bug", cmd.Use)

	sub, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "create", sub.Name())

	// Args: exactly one.
	assert.Error(t, sub.Args(sub, []string{}))
	assert.NoError(t, sub.Args(sub, []string{"title"}))
	assert.Error(t, sub.Args(sub, []string{"a", "b"}))

	assert.NotNil(t, sub.Flags().Lookup("description"))
	assert.Nil(t, sub.Flags().Lookup("project"))
	assert.Nil(t, sub.Flags().Lookup("type"))
}

func TestBuildSecurityCmd_structure(t *testing.T) {
	cmd := BuildSecurityCmd()
	assert.Equal(t, "security", cmd.Use)

	sub, _, err := cmd.Find([]string{"create"})
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "create", sub.Name())

	assert.NotNil(t, sub.Flags().Lookup("description"))
	assert.Nil(t, sub.Flags().Lookup("project"))
	assert.Nil(t, sub.Flags().Lookup("type"))
}

func TestRunBugCreate_success(t *testing.T) {
	token := "tok"
	var got daemon.BugCreateRequest
	addr := startTestDaemon(t, token,
		func(req daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
			got = req
			return daemon.BugCreateResponse{Key: "SC-9", URL: "https://t/SC-9"}, nil
		}, nil)
	pointEnvAtDaemon(t, addr, token)

	var out bytes.Buffer
	require.NoError(t, RunBugCreate(&out, "A bug title", ""))
	assert.Equal(t, "SC-9\thttps://t/SC-9\n", out.String())
	assert.Equal(t, "A bug title", got.Title)
}

func TestRunBugCreate_forwardsDescription(t *testing.T) {
	token := "tok"
	var got daemon.BugCreateRequest
	addr := startTestDaemon(t, token,
		func(req daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
			got = req
			return daemon.BugCreateResponse{Key: "SC-9", URL: "https://t/SC-9"}, nil
		}, nil)
	pointEnvAtDaemon(t, addr, token)

	var out bytes.Buffer
	require.NoError(t, RunBugCreate(&out, "t", "some detail"))
	assert.Equal(t, "some detail", got.Description)
}

func TestRunBugCreate_emptyTitle(t *testing.T) {
	var out bytes.Buffer
	err := RunBugCreate(&out, "   ", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bug title must not be empty")
	assert.Empty(t, out.String())
}

func TestRunBugCreate_creatorError(t *testing.T) {
	token := "tok"
	addr := startTestDaemon(t, token,
		func(req daemon.BugCreateRequest) (daemon.BugCreateResponse, error) {
			return daemon.BugCreateResponse{}, errors.WithDetails("no PM-role tracker configured")
		}, nil)
	pointEnvAtDaemon(t, addr, token)

	var out bytes.Buffer
	err := RunBugCreate(&out, "title", "")
	require.Error(t, err)
	// The route surfaces the creator's message via the response stderr, which the
	// client carries as a structured detail rather than in the top-level message.
	assert.Contains(t, errors.AllDetails(err)["stderr"], "no PM-role tracker configured")
}

func TestRunBugCreate_noDaemon(t *testing.T) {
	// Discovery is replaced outright: in CI or a devcontainer a real host daemon
	// answers on the well-known fallback address, so the no-daemon case cannot be
	// created by taking the environment away.
	orig := connectDaemon
	connectDaemon = func() (*daemon.Client, error) {
		return nil, errors.WithDetails("human daemon not reachable — start it with `human daemon start`")
	}
	t.Cleanup(func() { connectDaemon = orig })

	var out bytes.Buffer
	err := RunBugCreate(&out, "title", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "human daemon not reachable")
}

func TestRunSecurityCreate_success(t *testing.T) {
	token := "tok"
	var got daemon.SecurityCreateRequest
	addr := startTestDaemon(t, token, nil,
		func(req daemon.SecurityCreateRequest) (daemon.SecurityCreateResponse, error) {
			got = req
			return daemon.SecurityCreateResponse{Key: "SC-10", URL: "https://t/SC-10"}, nil
		})
	pointEnvAtDaemon(t, addr, token)

	var out bytes.Buffer
	require.NoError(t, RunSecurityCreate(&out, "A security title", "detail"))
	assert.Equal(t, "SC-10\thttps://t/SC-10\n", out.String())
	assert.Equal(t, "detail", got.Description)
}

func TestRunSecurityCreate_emptyTitle(t *testing.T) {
	var out bytes.Buffer
	err := RunSecurityCreate(&out, "   ", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "security ticket title must not be empty")
	assert.Empty(t, out.String())
}
