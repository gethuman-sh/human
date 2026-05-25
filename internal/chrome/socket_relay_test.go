package chrome

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketRelay_HappyPath(t *testing.T) {
	dir := shortTempDir(t)
	relay := NewSocketRelay(dir, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenReady := make(chan struct{})
	go func() {
		// Wait for socket file to appear, then signal ready.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
			if len(matches) > 0 {
				close(listenReady)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	go func() {
		_ = relay.ListenAndServe(ctx)
	}()

	<-listenReady

	// Simulate Chrome native host connecting.
	matches, err := filepath.Glob(filepath.Join(dir, "*.sock"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	chromeConn, err := net.Dial("unix", matches[0])
	require.NoError(t, err)
	defer func() { _ = chromeConn.Close() }()

	// Spawn should dequeue the connection.
	stdin, stdout, wait, sErr := relay.Spawn(ctx)
	require.NoError(t, sErr)
	defer func() { _ = wait() }()

	// Chrome → bridge direction.
	_, err = chromeConn.Write([]byte("from-chrome"))
	require.NoError(t, err)
	_ = chromeConn.(*net.UnixConn).CloseWrite()

	data, err := io.ReadAll(stdout)
	require.NoError(t, err)
	assert.Equal(t, "from-chrome", string(data))

	// Bridge → Chrome direction.
	_, err = stdin.Write([]byte("to-chrome"))
	require.NoError(t, err)
	_ = stdin.Close()

	received, err := io.ReadAll(chromeConn)
	require.NoError(t, err)
	assert.Equal(t, "to-chrome", string(received))
}

func TestSocketRelay_ChromeBeforeSpawn(t *testing.T) {
	dir := shortTempDir(t)
	relay := NewSocketRelay(dir, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenReady := make(chan struct{})
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
			if len(matches) > 0 {
				close(listenReady)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	go func() {
		_ = relay.ListenAndServe(ctx)
	}()

	<-listenReady

	matches, err := filepath.Glob(filepath.Join(dir, "*.sock"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	// Chrome connects first. Retry to avoid race between file creation and listener readiness.
	var chromeConn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		chromeConn, err = net.Dial("unix", matches[0])
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, err)
	defer func() { _ = chromeConn.Close() }()

	// Give the relay time to queue the connection.
	time.Sleep(50 * time.Millisecond)

	// Then Spawn dequeues it.
	_, _, wait, sErr := relay.Spawn(ctx)
	require.NoError(t, sErr)
	_ = wait()
}

func TestSocketRelay_SpawnBlocksUntilChrome(t *testing.T) {
	dir := shortTempDir(t)
	relay := NewSocketRelay(dir, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenReady := make(chan struct{})
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
			if len(matches) > 0 {
				close(listenReady)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	go func() {
		_ = relay.ListenAndServe(ctx)
	}()

	<-listenReady

	matches, err := filepath.Glob(filepath.Join(dir, "*.sock"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	// Start Spawn in a goroutine — it should block.
	spawnDone := make(chan struct{})
	var spawnErr error
	go func() {
		_, _, wait, sErr := relay.Spawn(ctx)
		spawnErr = sErr
		if sErr == nil {
			_ = wait()
		}
		close(spawnDone)
	}()

	// Verify Spawn hasn't returned yet.
	select {
	case <-spawnDone:
		t.Fatal("Spawn returned before Chrome connected")
	case <-time.After(100 * time.Millisecond):
		// Expected: still blocking.
	}

	// Now Chrome connects.
	chromeConn, err := net.Dial("unix", matches[0])
	require.NoError(t, err)
	_ = chromeConn.Close()

	// Spawn should complete.
	select {
	case <-spawnDone:
		require.NoError(t, spawnErr)
	case <-time.After(2 * time.Second):
		t.Fatal("Spawn did not return after Chrome connected")
	}
}

func TestSocketRelay_SpawnContextCancellation(t *testing.T) {
	dir := shortTempDir(t)
	relay := NewSocketRelay(dir, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_ = relay.ListenAndServe(ctx)
		close(done)
	}()

	// Cancel before any Chrome connection.
	spawnCtx, spawnCancel := context.WithCancel(context.Background())
	spawnCancel()

	_, _, _, err := relay.Spawn(spawnCtx)
	require.Error(t, err)

	cancel()
	<-done // wait for ListenAndServe cleanup before TempDir removal
}

func TestSocketRelay_CleanShutdown(t *testing.T) {
	dir := shortTempDir(t)
	relay := NewSocketRelay(dir, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())

	listenReady := make(chan struct{})
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			matches, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
			if len(matches) > 0 {
				close(listenReady)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- relay.ListenAndServe(ctx)
	}()

	<-listenReady

	matches, err := filepath.Glob(filepath.Join(dir, "*.sock"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	// Queue a connection, then cancel.
	chromeConn, err := net.Dial("unix", matches[0])
	require.NoError(t, err)
	// Don't close — let shutdown drain it.
	_ = chromeConn

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case sErr := <-done:
		require.NoError(t, sErr)
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return on cancel")
	}

	// Socket file should be removed.
	_, err = os.Stat(matches[0])
	assert.True(t, os.IsNotExist(err), "socket file should be removed after shutdown")
}
