package gui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/agent"
	"github.com/gethuman-sh/human/internal/daemon"
)

type fakeCleaner struct {
	mu            sync.Mutex
	decommed      string
	stoppedID     string
	containerID   string
	decommErr     error
	stopContainer chan struct{}
}

func (f *fakeCleaner) DeleteAgent(context.Context, string) error { return nil }

func (f *fakeCleaner) DecommissionAgent(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.decommed = name
	return f.containerID, f.decommErr
}

func (f *fakeCleaner) StopContainer(_ context.Context, containerID string) error {
	f.mu.Lock()
	f.stoppedID = containerID
	f.mu.Unlock()
	if f.stopContainer != nil {
		close(f.stopContainer)
	}
	return nil
}

// waitForEvent polls the store until an event with the given name appears.
func waitForEvent(t *testing.T, store *daemon.HookEventStore, eventName string) {
	t.Helper()
	require.Eventually(t, func() bool {
		events, _ := store.EventsSince(0)
		for _, e := range events {
			if e.EventName == eventName {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
}

func TestManagerRunner_DispatchSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := daemon.NewHookEventStore()

	var gotOpts agent.StartOpts
	var mu sync.Mutex
	r := &ManagerRunner{
		Hooks:  store,
		Logger: zerolog.Nop(),
		Start: func(_ context.Context, opts agent.StartOpts) error {
			mu.Lock()
			gotOpts = opts
			mu.Unlock()
			return nil
		},
	}

	name, err := r.Dispatch(context.Background(), DispatchOpts{
		Prompt:     "/human-execute HUM-1",
		ProjectDir: "/home/u/proj",
	})
	require.NoError(t, err)
	assert.Equal(t, "agent-1", name)

	waitForEvent(t, store, "AgentStarted")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "agent-1", gotOpts.Name)
	assert.Equal(t, "/human-execute HUM-1", gotOpts.Prompt)
	assert.Equal(t, "/home/u/proj", gotOpts.Workspace)
	// Headless dispatch always skips permission prompts — nobody is
	// attached to answer them.
	assert.True(t, gotOpts.SkipPerms)
}

func TestManagerRunner_DispatchFailureEmitsEvent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := daemon.NewHookEventStore()

	r := &ManagerRunner{
		Hooks:  store,
		Logger: zerolog.Nop(),
		Start: func(context.Context, agent.StartOpts) error {
			return errors.WithDetails("docker down")
		},
	}

	_, err := r.Dispatch(context.Background(), DispatchOpts{Prompt: "/human-plan 7"})
	require.NoError(t, err) // dispatch itself succeeds; failure is async

	waitForEvent(t, store, "AgentStartFailed")
}

func TestManagerRunner_DispatchRejectsEmptyPrompt(t *testing.T) {
	r := &ManagerRunner{Logger: zerolog.Nop()}
	_, err := r.Dispatch(context.Background(), DispatchOpts{})
	assert.Error(t, err)
}

func TestManagerRunner_Stop(t *testing.T) {
	store := daemon.NewHookEventStore()
	cleaner := &fakeCleaner{containerID: "c-123", stopContainer: make(chan struct{})}
	r := &ManagerRunner{Hooks: store, Cleaner: cleaner, Logger: zerolog.Nop()}

	require.NoError(t, r.Stop(context.Background(), "agent-2"))
	assert.Equal(t, "agent-2", cleaner.decommed)

	waitForEvent(t, store, "AgentStopped")

	// Container teardown runs in the background.
	select {
	case <-cleaner.stopContainer:
	case <-time.After(2 * time.Second):
		t.Fatal("expected background container stop")
	}
	cleaner.mu.Lock()
	defer cleaner.mu.Unlock()
	assert.Equal(t, "c-123", cleaner.stoppedID)
}

func TestManagerRunner_StopErrors(t *testing.T) {
	r := &ManagerRunner{Logger: zerolog.Nop()}
	assert.Error(t, r.Stop(context.Background(), "agent-1"))

	r.Cleaner = &fakeCleaner{decommErr: errors.WithDetails("no meta")}
	assert.Error(t, r.Stop(context.Background(), "agent-1"))
}
