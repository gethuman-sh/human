//go:build !windows

package chrome

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sockPathFor is where a relay in this process listens.
func sockPathFor(dir string) string {
	return filepath.Join(dir, fmtPid())
}

func fmtPid() string {
	return itoa(os.Getpid()) + ".sock"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestRelayRetireRemovesSocket proves the outgoing daemon's relay stops being
// discoverable the moment it retires: a client globbing the socket directory
// during a self-restart must not be able to attach to the dying process.
func TestRelayRetireRemovesSocket(t *testing.T) {
	dir := t.TempDir()
	relay := NewSocketRelay(dir, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- relay.ListenAndServe(ctx) }()

	sock := sockPathFor(dir)
	require.Eventually(t, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "relay never created its socket")

	// It is reachable while serving.
	conn, err := net.DialTimeout("unix", sock, time.Second)
	require.NoError(t, err)
	_ = conn.Close()

	relay.Retire()

	// The socket file is gone, so a glob-based discovery cannot find it.
	_, statErr := os.Stat(sock)
	assert.True(t, os.IsNotExist(statErr), "retired relay must remove its socket file")

	// And it no longer accepts.
	_, dialErr := net.DialTimeout("unix", sock, 200*time.Millisecond)
	assert.Error(t, dialErr, "retired relay must stop accepting")

	cancel()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after cancel")
	}
}

func TestRelayRetireIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	relay := NewSocketRelay(dir, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.ListenAndServe(ctx) }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(sockPathFor(dir))
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	relay.Retire()
	relay.Retire() // must not panic or error
}

// TestRelayRetireBeforeListen covers the race where a handover retires the
// relay before it finished binding: it must not leave an orphan socket behind.
func TestRelayRetireBeforeListen(t *testing.T) {
	dir := t.TempDir()
	relay := NewSocketRelay(dir, zerolog.Nop())
	relay.Retire() // retired before ListenAndServe ever runs

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- relay.ListenAndServe(ctx) }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return for an already-retired relay")
	}
	_, statErr := os.Stat(sockPathFor(dir))
	assert.True(t, os.IsNotExist(statErr), "a retired relay must not leave its socket behind")
}

// TestRelayActiveConnsTracksQueue proves a queued Chrome connection counts as
// in-flight, so a handover drain waits for it instead of cutting it off.
func TestRelayActiveConnsTracksQueue(t *testing.T) {
	dir := t.TempDir()
	relay := NewSocketRelay(dir, zerolog.Nop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = relay.ListenAndServe(ctx) }()

	sock := sockPathFor(dir)
	require.Eventually(t, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	assert.Equal(t, int64(0), relay.ActiveConns())

	conn, err := net.DialTimeout("unix", sock, time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	require.Eventually(t, func() bool {
		return relay.ActiveConns() == 1
	}, 2*time.Second, 10*time.Millisecond, "a queued chrome connection must count as in flight")

	// Pairing it with a bridge keeps it counted until the pairing ends.
	_, _, wait, err := relay.Spawn(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), relay.ActiveConns())

	_ = wait()
	assert.Equal(t, int64(0), relay.ActiveConns(), "a finished pairing must stop counting")
}
