package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_HandleDaemonBusy_NilLeaseCheckerReportsIdle(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) { s.LeaseChecker = nil })

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"daemon-busy"}})
	require.Equal(t, 0, resp.ExitCode)

	var status DaemonBusyStatus
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &status))
	assert.False(t, status.Busy)
}

func TestServer_HandleDaemonBusy_ReportsBusyWhenLeaseCheckerSaysSo(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.LeaseChecker = func(ctx context.Context, project string) (bool, error) {
			assert.Equal(t, "", project, "single-project registry resolves to the default project")
			return true, nil
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"daemon-busy"}})
	require.Equal(t, 0, resp.ExitCode)

	var status DaemonBusyStatus
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &status))
	assert.True(t, status.Busy)
}

func TestServer_HandleDaemonBusy_LeaseCheckerErrorIsReported(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.LeaseChecker = func(ctx context.Context, project string) (bool, error) {
			return false, assert.AnError
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"daemon-busy"}})
	assert.NotEqual(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr+resp.Stdout, assert.AnError.Error())
}

// With 2+ registered projects, the whole-daemon check names the first one —
// matching boardProjectKey's existing "first registered project" convention.
func TestServer_HandleDaemonBusy_MultiProjectUsesFirstRegisteredProject(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dirA, ".humanconfig.yaml"), []byte("project: alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirB, ".humanconfig.yaml"), []byte("project: beta\n"), 0o644))
	reg, err := NewProjectRegistry([]string{dirA, dirB})
	require.NoError(t, err)

	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.Projects = reg
		s.LeaseChecker = func(ctx context.Context, project string) (bool, error) {
			assert.Equal(t, "alpha", project)
			return false, nil
		}
	})

	// Cwd deliberately resolves to the SECOND registered project (beta) — this
	// proves the whole-daemon busy check ignores the request's own project
	// routing and always names the first registered project, matching
	// defaultProjectName's documented convention. The route still needs a Cwd
	// that resolves to SOME registered project, or resolveProjectDir rejects
	// the request before it ever reaches the handler.
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"daemon-busy"}, Cwd: dirB})
	require.Equal(t, 0, resp.ExitCode)
}
