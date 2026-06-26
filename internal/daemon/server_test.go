package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	client "github.com/gethuman-sh/human-daemon-client"
	"github.com/gethuman-sh/human/internal/env"
	"github.com/gethuman-sh/human/internal/proxy"
	"github.com/gethuman-sh/human/internal/stats"
	"github.com/gethuman-sh/human/internal/tracker"
)

func echoCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "test",
		SilenceUsage: true,
	}
	root.AddCommand(&cobra.Command{
		Use: "echo",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(args, " "))
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use: "fail",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("intentional error")
		},
	})
	return root
}

func startTestServerWithOpts(t *testing.T, token string, safeMode bool) (addr string, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		SafeMode:   safeMode,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr = ln.Addr().String()
	_ = ln.Close()

	srv.Addr = addr

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	// Wait for server to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Cleanup(func() { cancel() })
	return addr, cancel
}

func startTestServer(t *testing.T, token string) (addr string, cancel context.CancelFunc) {
	t.Helper()
	return startTestServerWithOpts(t, token, false)
}

func sendRequest(t *testing.T, addr string, req Request) Response {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	enc := json.NewEncoder(conn)
	require.NoError(t, enc.Encode(req))

	scanner := bufio.NewScanner(conn)
	require.True(t, scanner.Scan(), "expected response line")

	var resp Response
	require.NoError(t, json.Unmarshal(scanner.Bytes(), &resp))
	return resp
}

func TestServer_EchoCommand(t *testing.T) {
	token := "test-token-1234"
	addr, _ := startTestServer(t, token)

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"echo", "hello", "world"},
	})

	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "hello world\n", resp.Stdout)
	assert.Empty(t, resp.Stderr)
}

func TestServer_FailCommand(t *testing.T) {
	token := "test-token-1234"
	addr, _ := startTestServer(t, token)

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"fail"},
	})

	assert.Equal(t, 1, resp.ExitCode)
}

func TestServer_AuthRejection(t *testing.T) {
	token := "correct-token"
	addr, _ := startTestServer(t, token)

	resp := sendRequest(t, addr, Request{
		Token: "wrong-token",
		Args:  []string{"echo", "should-not-appear"},
	})

	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "authentication failed")
	assert.Empty(t, resp.Stdout)
}

func TestServer_ConcurrentRequests(t *testing.T) {
	token := "test-token-concurrent"
	addr, _ := startTestServer(t, token)

	var wg sync.WaitGroup
	results := make([]Response, 10)

	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = sendRequest(t, addr, Request{
				Token: token,
				Args:  []string{"echo", fmt.Sprintf("msg-%d", idx)},
			})
		}(i)
	}

	wg.Wait()

	for i, resp := range results {
		assert.Equal(t, 0, resp.ExitCode, "request %d failed", i)
		assert.Contains(t, resp.Stdout, fmt.Sprintf("msg-%d", i))
	}
}

func TestServer_InvalidJSON(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServer(t, token)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte("not json\n"))
	require.NoError(t, err)

	scanner := bufio.NewScanner(conn)
	require.True(t, scanner.Scan())

	var resp Response
	require.NoError(t, json.Unmarshal(scanner.Bytes(), &resp))
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid request JSON")
}

func safeCmdFactory() *cobra.Command {
	root := &cobra.Command{
		Use:          "test",
		SilenceUsage: true,
	}
	root.PersistentFlags().Bool("safe", false, "safe mode")
	root.AddCommand(&cobra.Command{
		Use: "check",
		RunE: func(cmd *cobra.Command, _ []string) error {
			safe, _ := cmd.Root().PersistentFlags().GetBool("safe")
			if !safe {
				// HUMAN_SAFE_MODE flows via the per-request env on cmd.Context();
				// the daemon never mutates os.Environ.
				safe = env.Lookup(cmd.Context(), "HUMAN_SAFE_MODE") == "1"
			}
			if safe {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "safe-mode-active")
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "safe-mode-inactive")
			}
			return nil
		},
	})
	return root
}

func TestServer_SafeModeSetsEnvVar(t *testing.T) {
	token := "test-token-safe"

	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		SafeMode:   true,
		CmdFactory: safeCmdFactory,
		Logger:     zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { cancel() })

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"check"},
	})

	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stdout, "safe-mode-active")
}

func TestServer_SafeModeDisabled(t *testing.T) {
	token := "test-token-nosafe"

	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		SafeMode:   false,
		CmdFactory: safeCmdFactory,
		Logger:     zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { cancel() })

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"check"},
	})

	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stdout, "safe-mode-inactive")
}

func envCmdFactory() *cobra.Command {
	root := &cobra.Command{
		Use:          "test",
		SilenceUsage: true,
	}
	root.AddCommand(&cobra.Command{
		Use: "env",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Read from the per-request env carried on the cobra context.
			// The daemon no longer mutates os.Environ; values must be
			// looked up via env.Lookup so they remain isolated per request.
			v := env.Lookup(cmd.Context(), "NO_COLOR")
			if v == "" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "NO_COLOR=<unset>")
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "NO_COLOR=%s\n", v)
			}
			return nil
		},
	})
	return root
}

func TestServer_EnvApplied(t *testing.T) {
	t.Setenv("NO_COLOR", "original")

	token := "test-token-env"
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: envCmdFactory,
		Logger:     zerolog.Nop(),
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { cancel() })

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"env"},
		Env:   map[string]string{"NO_COLOR": "from-client"},
	})

	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stdout, "NO_COLOR=from-client")

	// Verify the daemon never mutated the process environment.
	assert.Equal(t, "original", os.Getenv("NO_COLOR"))
}

// TestServer_ConcurrentEnvIsolation proves that two concurrent requests
// with different env values do not contaminate each other. Before the
// per-request env refactor, this would have been racy because both
// requests mutated the same process environment under a single mutex.
func TestServer_ConcurrentEnvIsolation(t *testing.T) {
	t.Setenv("NO_COLOR", "outer")

	token := "test-token-conc"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Slow command: introduces a small delay between context lookup and
	// reply so a buggy implementation would observe interleaved values.
	slowFactory := func() *cobra.Command {
		root := &cobra.Command{Use: "test", SilenceUsage: true}
		root.AddCommand(&cobra.Command{
			Use: "env",
			RunE: func(cmd *cobra.Command, _ []string) error {
				v := env.Lookup(cmd.Context(), "NO_COLOR")
				time.Sleep(20 * time.Millisecond)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "NO_COLOR=%s\n", v)
				return nil
			},
		})
		return root
	}

	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: slowFactory,
		Logger:     zerolog.Nop(),
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()

	// Wait for the listener.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	const goroutines = 16
	results := make([]string, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			value := fmt.Sprintf("client-%d", idx)
			resp := sendRequest(t, addr, Request{
				Token: token,
				Args:  []string{"env"},
				Env:   map[string]string{"NO_COLOR": value},
			})
			results[idx] = strings.TrimSpace(resp.Stdout)
		}(i)
	}
	wg.Wait()

	for i := 0; i < goroutines; i++ {
		expected := fmt.Sprintf("NO_COLOR=client-%d", i)
		assert.Equal(t, expected, results[i],
			"goroutine %d observed wrong env value (cross-request contamination)", i)
	}

	// And the daemon must not have leaked any value into the process env.
	assert.Equal(t, "outer", os.Getenv("NO_COLOR"))
}

func TestServer_TracksClientPID(t *testing.T) {
	token := "test-token"
	tracker := NewConnectedTracker()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &Server{
		Addr:          "127.0.0.1:0",
		Token:         token,
		CmdFactory:    echoCmd,
		Logger:        zerolog.Nop(),
		ConnectedPIDs: tracker,
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)

	assert.Empty(t, tracker.PIDs())

	resp := sendRequest(t, addr, Request{
		Token:     token,
		Args:      []string{"echo", "hi"},
		ClientPID: 12345,
	})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, []int{12345}, tracker.PIDs())

	// Second request with different PID.
	resp = sendRequest(t, addr, Request{
		Token:     token,
		Args:      []string{"echo", "hi"},
		ClientPID: 67890,
	})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, []int{12345, 67890}, tracker.PIDs())
}

func TestServer_IgnoresZeroClientPID(t *testing.T) {
	token := "test-token"
	tracker := NewConnectedTracker()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := &Server{
		Addr:          "127.0.0.1:0",
		Token:         token,
		CmdFactory:    echoCmd,
		Logger:        zerolog.Nop(),
		ConnectedPIDs: tracker,
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)

	resp := sendRequest(t, addr, Request{
		Token:     token,
		Args:      []string{"echo", "hi"},
		ClientPID: 0,
	})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Empty(t, tracker.PIDs())
}

// --- detectDestructive tests ---

func TestDetectDestructive_Delete(t *testing.T) {
	op, ok := detectDestructive([]string{"jira", "issue", "delete", "KAN-1"})
	assert.True(t, ok)
	assert.Equal(t, "DeleteIssue", op.Operation)
	assert.Equal(t, "jira", op.Tracker)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_Edit(t *testing.T) {
	op, ok := detectDestructive([]string{"linear", "issue", "edit", "HUM-42", "--title", "New"})
	assert.True(t, ok)
	assert.Equal(t, "EditIssue", op.Operation)
	assert.Equal(t, "linear", op.Tracker)
	assert.Equal(t, "HUM-42", op.Key)
}

func TestDetectDestructive_WithSafePrefix(t *testing.T) {
	op, ok := detectDestructive([]string{"--safe", "jira", "issue", "delete", "KAN-1"})
	assert.True(t, ok)
	assert.Equal(t, "DeleteIssue", op.Operation)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_YesDoesNotSkip(t *testing.T) {
	// --yes is ignored by the daemon — confirmation always required via TUI.
	op, ok := detectDestructive([]string{"jira", "issue", "delete", "KAN-1", "--yes"})
	assert.True(t, ok)
	assert.Equal(t, "DeleteIssue", op.Operation)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_NonDestructive(t *testing.T) {
	_, ok := detectDestructive([]string{"jira", "issue", "list", "--project", "KAN"})
	assert.False(t, ok)
}

func TestDetectDestructive_TooShort(t *testing.T) {
	_, ok := detectDestructive([]string{"jira", "issue"})
	assert.False(t, ok)
}

func TestDetectDestructive_NoIssueSubcommand(t *testing.T) {
	_, ok := detectDestructive([]string{"echo", "hello"})
	assert.False(t, ok)
}

func TestDetectDestructive_FlagInsertionBypass(t *testing.T) {
	// Flags between "issue" and "delete" must not break detection.
	op, ok := detectDestructive([]string{"jira", "issue", "--tracker=jira", "delete", "KAN-1"})
	assert.True(t, ok)
	assert.Equal(t, "DeleteIssue", op.Operation)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_ArbitraryFlagsStripped(t *testing.T) {
	op, ok := detectDestructive([]string{"--verbose", "linear", "--format=json", "issue", "edit", "HUM-1", "--title", "New"})
	assert.True(t, ok)
	assert.Equal(t, "EditIssue", op.Operation)
	assert.Equal(t, "linear", op.Tracker)
	assert.Equal(t, "HUM-1", op.Key)
}

func TestDetectDestructive_SpaceSeparatedValueFlagBypass(t *testing.T) {
	// A space-separated global value flag ("--tracker jira") must not shift the
	// positional indices and let the delete slip past detection.
	op, ok := detectDestructive([]string{"jira", "issue", "--tracker", "jira", "delete", "KAN-1"})
	assert.True(t, ok)
	assert.Equal(t, "DeleteIssue", op.Operation)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_StatusTransition(t *testing.T) {
	op, ok := detectDestructive([]string{"jira", "issue", "status", "KAN-1", "Done"})
	assert.True(t, ok)
	assert.Equal(t, "TransitionIssue", op.Operation)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_Start(t *testing.T) {
	op, ok := detectDestructive([]string{"jira", "issue", "start", "KAN-1"})
	assert.True(t, ok)
	assert.Equal(t, "StartIssue", op.Operation)
	assert.Equal(t, "KAN-1", op.Key)
}

func TestDetectDestructive_StatusesListNotDestructive(t *testing.T) {
	// The read-only "statuses" listing verb must not be intercepted.
	_, ok := detectDestructive([]string{"jira", "issue", "statuses", "KAN-1"})
	assert.False(t, ok)
}

// --- Server destructive confirmation tests ---

func startTestServerWithConfirm(t *testing.T, token string) (addr string, cancel context.CancelFunc, store *PendingConfirmStore) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store = NewPendingConfirmStore()

	srv := &Server{
		Addr:            "127.0.0.1:0",
		Token:           token,
		CmdFactory:      echoCmd,
		Logger:          zerolog.Nop(),
		PendingConfirms: store,
	}

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr = ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { cancel() })
	return addr, cancel, store
}

func TestServer_DestructiveConfirm_Approved(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	// Send a destructive command in a goroutine — it will block.
	type result struct {
		resp1 Response
		resp2 Response
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer func() { _ = conn.Close() }()

		enc := json.NewEncoder(conn)
		_ = enc.Encode(Request{Token: token, ClientPID: 1111, Args: []string{"jira", "issue", "delete", "KAN-1"}})

		reader := bufio.NewReader(conn)
		line1, err := reader.ReadBytes('\n')
		if err != nil {
			ch <- result{err: err}
			return
		}
		var r1 Response
		_ = json.Unmarshal(line1, &r1)

		line2, err := reader.ReadBytes('\n')
		if err != nil {
			ch <- result{err: err}
			return
		}
		var r2 Response
		_ = json.Unmarshal(line2, &r2)

		ch <- result{resp1: r1, resp2: r2}
	}()

	// Wait for the pending confirmation to appear.
	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, 1, store.Len())

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "DeleteIssue", snap[0].Operation)
	assert.Equal(t, "KAN-1", snap[0].Key)

	// Approve it as a distinct client (different PID from the requester).
	err := store.Resolve(snap[0].ID, true, 2222)
	require.NoError(t, err)

	r := <-ch
	require.NoError(t, r.err)
	assert.True(t, r.resp1.AwaitConfirm)
	assert.Contains(t, r.resp1.ConfirmPrompt, "KAN-1")
	// Line 2: the command executed (echo cmd handles "issue delete KAN-1 --yes" as unknown, so exit 1 is fine)
	// The important thing is we got two lines.
	assert.NotEmpty(t, r.resp1.ConfirmID)
}

func TestServer_DestructiveConfirm_Rejected(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	type result struct {
		resp1 Response
		resp2 Response
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer func() { _ = conn.Close() }()

		enc := json.NewEncoder(conn)
		_ = enc.Encode(Request{Token: token, ClientPID: 1111, Args: []string{"jira", "issue", "delete", "KAN-2"}})

		reader := bufio.NewReader(conn)
		line1, _ := reader.ReadBytes('\n')
		var r1 Response
		_ = json.Unmarshal(line1, &r1)

		line2, _ := reader.ReadBytes('\n')
		var r2 Response
		_ = json.Unmarshal(line2, &r2)

		ch <- result{resp1: r1, resp2: r2}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	snap := store.Snapshot()
	require.Len(t, snap, 1)

	// Reject it as a distinct client.
	err := store.Resolve(snap[0].ID, false, 2222)
	require.NoError(t, err)

	r := <-ch
	require.NoError(t, r.err)
	assert.True(t, r.resp1.AwaitConfirm)
	assert.Contains(t, r.resp2.Stderr, "aborted")
	assert.Equal(t, 1, r.resp2.ExitCode)
}

func TestServer_DestructiveYes_StillRequiresConfirmation(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	// --yes does NOT bypass daemon confirmation; the daemon always asks.
	type result struct {
		resp1 Response
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer func() { _ = conn.Close() }()

		enc := json.NewEncoder(conn)
		_ = enc.Encode(Request{Token: token, ClientPID: 1111, Args: []string{"jira", "issue", "delete", "KAN-3", "--yes"}})

		reader := bufio.NewReader(conn)
		line1, err := reader.ReadBytes('\n')
		if err != nil {
			ch <- result{err: err}
			return
		}
		var r1 Response
		_ = json.Unmarshal(line1, &r1)
		ch <- result{resp1: r1}
	}()

	// Confirmation should still be created.
	deadline := time.Now().Add(2 * time.Second)
	for store.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 1, store.Len())

	// Clean up: resolve it so the goroutine can finish. Use a distinct PID.
	snap := store.Snapshot()
	if len(snap) > 0 {
		_ = store.Resolve(snap[0].ID, false, 2222)
	}

	r := <-ch
	require.NoError(t, r.err)
	assert.True(t, r.resp1.AwaitConfirm)
}

func TestServer_PendingConfirmsEndpoint(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	// Initially empty.
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"pending-confirms"}})
	assert.Equal(t, "[]\n", resp.Stdout)

	// Add a pending confirmation manually.
	store.Add(&PendingConfirmation{
		ID:        "test-1",
		Operation: "DeleteIssue",
		Tracker:   "jira",
		Key:       "KAN-1",
		Prompt:    "Delete KAN-1?",
		CreatedAt: time.Now(),
		Decision:  make(chan bool, 1),
	})

	resp = sendRequest(t, addr, Request{Token: token, Args: []string{"pending-confirms"}})
	assert.Contains(t, resp.Stdout, "test-1")
	assert.Contains(t, resp.Stdout, "KAN-1")
}

func TestServer_ConfirmOpEndpoint(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	pc := &PendingConfirmation{
		ID:        "test-resolve",
		ClientPID: 1111,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	// Resolver sends a distinct PID from the requester.
	resp := sendRequest(t, addr, Request{Token: token, ClientPID: 2222, Args: []string{"confirm-op", "test-resolve", "yes"}})
	assert.Equal(t, "ok\n", resp.Stdout)

	decision := <-pc.Decision
	assert.True(t, decision)
}

func TestServer_ConfirmOpEndpoint_NotFound(t *testing.T) {
	token := "test-token"
	addr, _, _ := startTestServerWithConfirm(t, token)

	resp := sendRequest(t, addr, Request{Token: token, ClientPID: 2222, Args: []string{"confirm-op", "nonexistent", "yes"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "nonexistent")
}

// TestServer_ConfirmOpEndpoint_RejectsMissingPID verifies that a confirm-op
// request without a ClientPID is rejected — the daemon requires a positive
// approver PID from the resolving client.
func TestServer_ConfirmOpEndpoint_RejectsMissingPID(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	pc := &PendingConfirmation{
		ID:        "no-pid",
		ClientPID: 1111,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	// ClientPID omitted → defaults to 0 → request must be rejected.
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"confirm-op", "no-pid", "yes"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.NotEmpty(t, resp.Stderr)
	// The confirmation must still be pending — nothing was resolved.
	assert.Equal(t, 1, store.Len())
}

// Token comparison runs through subtle.ConstantTimeCompare, so only an
// exact byte-for-byte match authenticates. This test locks in that
// behavior for several near-matches that have historically been
// attempted — whitespace-padded, case-shifted, empty — so a future
// refactor cannot loosen the comparison without a failing test.
func TestServer_TokenRejection(t *testing.T) {
	token := "correct-token"
	addr, _ := startTestServer(t, token)

	cases := []struct {
		name string
		sent string
	}{
		{"empty", ""},
		{"wrong", "wrong-token"},
		{"trailing whitespace", "correct-token "},
		{"leading whitespace", " correct-token"},
		{"case difference", "CORRECT-TOKEN"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			resp := sendRequest(t, addr, Request{Token: tt.sent, Args: []string{"echo", "x"}})
			assert.Equal(t, 1, resp.ExitCode)
			assert.Contains(t, resp.Stderr, "authentication failed")
		})
	}
}

// The accept loop caps concurrent connections (server.go:72) to stop a
// malicious client from pinning every goroutine and starving real work.
// The test holds the cap full with slow-read connections and verifies
// that connections over the cap are closed promptly by the server
// rather than being queued indefinitely.
func TestServer_ConnectionLimit(t *testing.T) {
	token := "tok"
	addr, _ := startTestServer(t, token)

	// Open 64 conns and hold them open by not sending any request.
	// The server will block in bufio.Reader.ReadBytes waiting for '\n'
	// up to its 10s read deadline, so these stay in-flight for the
	// duration of this test.
	const cap = 64
	holders := make([]net.Conn, 0, cap)
	defer func() {
		for _, c := range holders {
			_ = c.Close()
		}
	}()
	for i := 0; i < cap; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		require.NoError(t, err)
		holders = append(holders, c)
	}

	// Give the server's accept loop a brief moment to process the 64
	// accepts before we dial the over-cap batch.
	time.Sleep(100 * time.Millisecond)

	// Now dial beyond the cap. These should be accepted at the TCP
	// layer but immediately closed by the server's default branch in
	// the accept loop, so a Read returns EOF quickly. Accepted
	// in-flight conns would instead stay open until their 10s deadline,
	// so a 500ms read with io.EOF distinguishes cleanly.
	const extra = 10
	var rejected int32
	var wg sync.WaitGroup
	wg.Add(extra)
	for i := 0; i < extra; i++ {
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				atomic.AddInt32(&rejected, 1)
				return
			}
			defer func() { _ = c.Close() }()
			_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			buf := make([]byte, 1)
			_, rerr := c.Read(buf)
			if errors.Is(rerr, io.EOF) {
				atomic.AddInt32(&rejected, 1)
			}
		}()
	}
	wg.Wait()

	assert.GreaterOrEqual(t, int(atomic.LoadInt32(&rejected)), extra-2,
		"expected most of the over-cap conns to be rejected quickly (cap=%d, extra=%d)", cap, extra)
}

// The self-approval guard lives in PendingConfirmStore.Resolve (at
// pending_confirm.go:66), and is already unit-tested at the store
// level. This test proves the same guard fires when driven through
// the confirm-op endpoint — so a future refactor that bypasses
// Resolve or builds a parallel code path still cannot let a requester
// approve their own destructive op.
func TestServer_ConfirmOpEndpoint_SelfApprovalRejected(t *testing.T) {
	token := "test-token"
	addr, _, store := startTestServerWithConfirm(t, token)

	const requesterPID = 12345
	pc := &PendingConfirmation{
		ID:        "self-approve",
		ClientPID: requesterPID,
		Decision:  make(chan bool, 1),
	}
	store.Add(pc)

	// Same PID as the requester — must be rejected via the endpoint.
	resp := sendRequest(t, addr, Request{
		Token:     token,
		ClientPID: requesterPID,
		Args:      []string{"confirm-op", "self-approve", "yes"},
	})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "approver PID matches requester PID")

	// The entry must still be pending — no decision was delivered.
	assert.Equal(t, 1, store.Len())
}

// --- helper: start a server with arbitrary Server fields ---

func startTestServerCustom(t *testing.T, token string, customize func(s *Server)) (addr string, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
	}
	customize(srv)

	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr = ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr

	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { cancel() })
	return addr, cancel
}

// --- handleLogMode tests ---

func TestServer_HandleLogMode_GetDefault(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	// Reset to known state.
	proxy.SetLogMode(proxy.LogModeOff)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"log-mode"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "off\n", resp.Stdout)
}

func TestServer_HandleLogMode_SetMeta(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	proxy.SetLogMode(proxy.LogModeOff)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"log-mode", "meta"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "meta\n", resp.Stdout)

	// Verify it was set.
	assert.Equal(t, proxy.LogModeMeta, proxy.GetLogMode())
}

func TestServer_HandleLogMode_SetFull(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	proxy.SetLogMode(proxy.LogModeOff)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"log-mode", "full"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "full\n", resp.Stdout)
	assert.Equal(t, proxy.LogModeFull, proxy.GetLogMode())
}

func TestServer_HandleLogMode_SetOff(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	proxy.SetLogMode(proxy.LogModeFull)

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"log-mode", "off"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "off\n", resp.Stdout)
	assert.Equal(t, proxy.LogModeOff, proxy.GetLogMode())
}

func TestServer_HandleLogMode_InvalidMode(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"log-mode", "bogus"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "unknown log mode")
}

// --- handleHookEvent tests ---

func TestServer_HandleHookEvent_WithStore(t *testing.T) {
	token := "test-token"
	store := NewHookEventStore()
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = store
	})

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"hook-event", "tool_start", "session-1", "/tmp", "", "Bash"},
	})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)

	events := store.RecentEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "tool_start", events[0].EventName)
	assert.Equal(t, "session-1", events[0].SessionID)
	assert.Equal(t, "/tmp", events[0].Cwd)
	assert.Equal(t, "Bash", events[0].ToolName)
}

func TestServer_HandleHookEvent_NilStore(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = nil
	})

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"hook-event", "tool_start", "session-1", "/tmp"},
	})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
}

func TestServer_HandleHookEvent_NoArgs(t *testing.T) {
	token := "test-token"
	store := NewHookEventStore()
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = store
	})

	resp := sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"hook-event"},
	})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)

	events := store.RecentEvents()
	require.Len(t, events, 1)
	// Event was stored with empty fields.
	assert.Empty(t, events[0].EventName)
}

// --- handleHookSnapshot tests ---

func TestServer_HandleHookSnapshot_WithEvents(t *testing.T) {
	token := "test-token"
	store := NewHookEventStore()
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = store
	})

	// First store some events.
	sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"hook-event", "tool_start", "sess-A", "/home/user/project", "", "Read"},
	})
	sendRequest(t, addr, Request{
		Token: token,
		Args:  []string{"hook-event", "tool_end", "sess-A", "/home/user/project", "", "Read"},
	})

	// Now snapshot.
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"hook-snapshot"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stdout, "sess-A")

	// Verify it's valid JSON.
	var snapshot map[string]interface{}
	err := json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &snapshot)
	require.NoError(t, err)
	assert.Contains(t, snapshot, "sess-A")
}

func TestServer_HandleHookSnapshot_NilStore(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = nil
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"hook-snapshot"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "{}\n", resp.Stdout)
}

func TestServer_HandleHookSnapshot_EmptyStore(t *testing.T) {
	token := "test-token"
	store := NewHookEventStore()
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = store
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"hook-snapshot"}})
	assert.Equal(t, 0, resp.ExitCode)
	// Empty map rendered as JSON.
	assert.Equal(t, "{}\n", resp.Stdout)
}

// --- handleTrackerDiagnose tests ---

func TestServer_HandleTrackerDiagnose_WithDiagnoser(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.TrackerDiagnoser = func(dir string) []tracker.TrackerStatus {
			return []tracker.TrackerStatus{
				{Name: "work", Kind: "linear", Label: "Linear", Working: true},
				{Name: "amazingcto", Kind: "jira", Label: "Jira", Working: false},
			}
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-diagnose"}})
	assert.Equal(t, 0, resp.ExitCode)

	var statuses []tracker.TrackerStatus
	err := json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &statuses)
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, "linear", statuses[0].Kind)
	assert.True(t, statuses[0].Working)
	assert.Equal(t, "jira", statuses[1].Kind)
	assert.False(t, statuses[1].Working)
}

func TestServer_HandleTrackerDiagnose_EmptyResult(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.TrackerDiagnoser = func(dir string) []tracker.TrackerStatus {
			return nil
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-diagnose"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "null\n", resp.Stdout)
}

func TestServer_HandleTrackerDiagnose_ReceivesProjectDir(t *testing.T) {
	token := "test-token"
	var receivedDir string
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.TrackerDiagnoser = func(dir string) []tracker.TrackerStatus {
			receivedDir = dir
			return nil
		}
	})

	// Without a ProjectRegistry, projectDir defaults to ".".
	sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-diagnose"}})
	assert.Equal(t, ".", receivedDir)
}

// --- handleTrackerIssues tests ---

func TestServer_HandleTrackerIssues_NilFetcher(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.IssueFetcher = nil
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-issues"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "[]\n", resp.Stdout)
}

func TestServer_HandleTrackerIssues_WithResults(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.IssueFetcher = func() ([]TrackerIssuesResult, error) {
			return []TrackerIssuesResult{
				{
					TrackerName: "work",
					TrackerKind: "linear",
					Project:     "HUM",
					Issues: []client.Issue{
						{Key: "HUM-1", Title: "First issue", Status: "In Progress"},
						{Key: "HUM-2", Title: "Second issue", Status: "Todo"},
					},
				},
			}, nil
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-issues"}})
	assert.Equal(t, 0, resp.ExitCode)

	var results []TrackerIssuesResult
	err := json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &results)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "linear", results[0].TrackerKind)
	assert.Equal(t, "HUM", results[0].Project)
	require.Len(t, results[0].Issues, 2)
	assert.Equal(t, "HUM-1", results[0].Issues[0].Key)
}

func TestServer_HandleTrackerIssues_FetcherError(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.IssueFetcher = func() ([]TrackerIssuesResult, error) {
			return nil, fmt.Errorf("network timeout")
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-issues"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "network timeout")
}

func TestServer_HandleTrackerIssues_EmptyResults(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.IssueFetcher = func() ([]TrackerIssuesResult, error) {
			return []TrackerIssuesResult{}, nil
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-issues"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "[]\n", resp.Stdout)
}

// --- handleToolStats tests ---

func TestServer_HandleToolStats_NilStore(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.StatsStore = nil
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tool-stats"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "{}\n", resp.Stdout)
}

func TestServer_HandleToolStats_WithData(t *testing.T) {
	token := "test-token"

	store, err := stats.NewStatsStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	// Insert some events.
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, store.InsertEvent(ctx, "sess-1", "tool_start", "Bash", "/tmp", "", now.Add(-1*time.Hour)))
	require.NoError(t, store.InsertEvent(ctx, "sess-1", "tool_end", "Bash", "/tmp", "", now.Add(-59*time.Minute)))
	require.NoError(t, store.InsertEvent(ctx, "sess-1", "tool_start", "Read", "/tmp", "", now.Add(-30*time.Minute)))

	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.StatsStore = store
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tool-stats"}})
	assert.Equal(t, 0, resp.ExitCode)

	var ts stats.ToolStats
	err = json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &ts)
	require.NoError(t, err)
	assert.Equal(t, 3, ts.TotalEvents)
	assert.NotEmpty(t, ts.ByTool)
}

func TestServer_HandleToolStats_EmptyDB(t *testing.T) {
	token := "test-token"

	store, err := stats.NewStatsStore(":memory:")
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.StatsStore = store
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tool-stats"}})
	assert.Equal(t, 0, resp.ExitCode)

	var ts stats.ToolStats
	err = json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &ts)
	require.NoError(t, err)
	assert.Equal(t, 0, ts.TotalEvents)
}

// --- routeIntercept tests ---

func TestServer_RouteIntercept_EmptyArgs(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	// Empty args should not be intercepted; falls through to command execution.
	// With no subcommand registered for empty args, the root cmd prints help and exits 0.
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{}})
	assert.Equal(t, 0, resp.ExitCode)
}

func TestServer_RouteIntercept_UnknownCommand(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {})

	// An unknown command should NOT be intercepted; falls through to cobra.
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"nonexistent"}})
	// Cobra returns exit code 1 for unknown subcommands.
	assert.Equal(t, 1, resp.ExitCode)
}

func TestServer_RouteIntercept_HookEventRoute(t *testing.T) {
	token := "test-token"
	store := NewHookEventStore()
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = store
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"hook-event", "tool_start", "s1", "/tmp"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Len(t, store.RecentEvents(), 1)
}

func TestServer_RouteIntercept_HookSnapshotRoute(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.HookEvents = NewHookEventStore()
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"hook-snapshot"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "{}\n", resp.Stdout)
}

func TestServer_RouteIntercept_TrackerDiagnoseRoute(t *testing.T) {
	token := "test-token"
	var called bool
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.TrackerDiagnoser = func(dir string) []tracker.TrackerStatus {
			called = true
			return []tracker.TrackerStatus{}
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-diagnose"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.True(t, called)
}

func TestServer_RouteIntercept_TrackerIssuesRoute(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.IssueFetcher = func() ([]TrackerIssuesResult, error) {
			return []TrackerIssuesResult{}, nil
		}
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-issues"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "[]\n", resp.Stdout)
}

func TestServer_RouteIntercept_ToolStatsRoute(t *testing.T) {
	token := "test-token"
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.StatsStore = nil
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"tool-stats"}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "{}\n", resp.Stdout)
}

// --- resolveProjectDir tests ---

func TestServer_ResolveProjectDir_NilRegistry(t *testing.T) {
	s := &Server{Projects: nil}
	dir, err := s.resolveProjectDir("/some/path")
	require.NoError(t, err)
	assert.Equal(t, ".", dir)
}

func TestServer_ResolveProjectDir_SingleProject(t *testing.T) {
	registry := &ProjectRegistry{
		entries: []ProjectEntry{
			{Name: "myproject", Dir: "/home/user/myproject"},
		},
	}
	s := &Server{Projects: registry}
	dir, err := s.resolveProjectDir("/whatever/path")
	require.NoError(t, err)
	assert.Equal(t, "/home/user/myproject", dir)
}

func TestServer_ResolveProjectDir_MultiProject_Match(t *testing.T) {
	registry := &ProjectRegistry{
		entries: []ProjectEntry{
			{Name: "backend", Dir: "/home/user/backend"},
			{Name: "frontend", Dir: "/home/user/frontend"},
		},
	}
	s := &Server{Projects: registry}

	dir, err := s.resolveProjectDir("/home/user/backend/src")
	require.NoError(t, err)
	assert.Equal(t, "/home/user/backend", dir)
}

func TestServer_ResolveProjectDir_MultiProject_NoMatch(t *testing.T) {
	registry := &ProjectRegistry{
		entries: []ProjectEntry{
			{Name: "backend", Dir: "/home/user/backend"},
			{Name: "frontend", Dir: "/home/user/frontend"},
		},
	}
	s := &Server{Projects: registry}

	_, err := s.resolveProjectDir("/home/other/unrelated")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match any registered project")
	assert.Contains(t, err.Error(), "backend")
	assert.Contains(t, err.Error(), "frontend")
}

func TestServer_ResolveProjectDir_MultiProject_CwdMatch(t *testing.T) {
	registry := &ProjectRegistry{
		entries: []ProjectEntry{
			{Name: "backend", Dir: "/home/user/backend"},
			{Name: "frontend", Dir: "/home/user/frontend"},
		},
	}
	s := &Server{Projects: registry}

	dir, err := s.resolveProjectDir("/home/user/frontend/components")
	require.NoError(t, err)
	assert.Equal(t, "/home/user/frontend", dir)
}

// --- integration: resolveProjectDir via full server request ---

func TestServer_ResolveProjectDir_SingleProject_Integration(t *testing.T) {
	token := "test-token"
	registry := &ProjectRegistry{
		entries: []ProjectEntry{
			{Name: "myproject", Dir: "/home/user/myproject"},
		},
	}
	var receivedDir string
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.Projects = registry
		s.TrackerDiagnoser = func(dir string) []tracker.TrackerStatus {
			receivedDir = dir
			return nil
		}
	})

	sendRequest(t, addr, Request{Token: token, Args: []string{"tracker-diagnose"}, Cwd: "/whatever"})
	assert.Equal(t, "/home/user/myproject", receivedDir)
}

func TestServer_ResolveProjectDir_MultiProject_ErrorIntegration(t *testing.T) {
	token := "test-token"
	registry := &ProjectRegistry{
		entries: []ProjectEntry{
			{Name: "backend", Dir: "/home/user/backend"},
			{Name: "frontend", Dir: "/home/user/frontend"},
		},
	}
	addr, _ := startTestServerCustom(t, token, func(s *Server) {
		s.Projects = registry
	})

	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"echo", "test"}, Cwd: "/unrelated"})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "does not match any registered project")
}
