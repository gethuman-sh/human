package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gethuman-sh/human/errors"
)

// nameHoldingDocker models the one Docker property the shared mock omits and
// this ticket turns on: a container name is exclusive until the container is
// removed, and a container whose init fails stays in "created" holding that
// name with the reason in State.Error. Without it, ContainerCreate always
// succeeds and the name conflict that masked the real failure cannot happen.
type nameHoldingDocker struct {
	*mockDockerClient
	initError  string
	mu         sync.Mutex
	nextID     int
	containers []ContainerSummary
	states     map[string]ContainerState
}

func newNameHoldingDocker(initError string) *nameHoldingDocker {
	return &nameHoldingDocker{
		mockDockerClient: &mockDockerClient{
			imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
		},
		initError: initError,
		states:    map[string]ContainerState{},
	}
}

func (d *nameHoldingDocker) ContainerCreate(ctx context.Context, opts ContainerCreateOptions) (string, error) {
	_, _ = d.mockDockerClient.ContainerCreate(ctx, opts)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.containers {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == opts.Name {
				return "", fmt.Errorf(
					"Error response from daemon: Conflict. The container name %q is already in use by container %q. "+
						"You have to remove (or rename) that container to be able to reuse that name",
					"/"+opts.Name, c.ID)
			}
		}
	}
	d.nextID++
	id := fmt.Sprintf("held-container-%d", d.nextID)
	d.containers = append(d.containers, ContainerSummary{
		ID: id, Names: []string{"/" + opts.Name}, Image: opts.Image,
		State: "created", Labels: opts.Labels,
	})
	d.states[id] = ContainerState{Status: "created"}
	return id, nil
}

// ContainerStart replays an init failure: Docker records the reason on the
// container and leaves it in "created" with StartedAt unset.
func (d *nameHoldingDocker) ContainerStart(ctx context.Context, id string) error {
	_ = d.mockDockerClient.ContainerStart(ctx, id)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[id] = ContainerState{Status: "created", ExitCode: 128, Error: d.initError}
	return fmt.Errorf("Error response from daemon: %s", d.initError)
}

func (d *nameHoldingDocker) ContainerRemove(ctx context.Context, id string, opts ContainerRemoveOptions) error {
	_ = d.mockDockerClient.ContainerRemove(ctx, id, opts)
	d.mu.Lock()
	defer d.mu.Unlock()
	kept := d.containers[:0]
	for _, c := range d.containers {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	d.containers = kept
	delete(d.states, id)
	return nil
}

func (d *nameHoldingDocker) ContainerInspect(_ context.Context, id string) (ContainerInspectResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.states[id]
	if !ok {
		return ContainerInspectResponse{}, fmt.Errorf("no such container: %s", id)
	}
	return ContainerInspectResponse{ID: id, State: st}, nil
}

func (d *nameHoldingDocker) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]ContainerSummary(nil), d.containers...), nil
}

// TestManager_Up_NeverStartedContainerIsReplacedAndReportsItsInitError is the
// SC-4632 regression: an agent container whose init fails is left in "created"
// holding the run's name. The relaunch must remove it and report what actually
// killed it, not the name it collides on.
func TestManager_Up_NeverStartedContainerIsReplacedAndReportsItsInitError(t *testing.T) {
	const initErr = `failed to create task for container: failed to create shim task: OCI runtime create failed: ` +
		`runc create failed: unable to start container process: error during container init: ` +
		`error mounting "/host/.git" to rootfs: no such file or directory: unknown`

	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := newNameHoldingDocker(initErr)
	mgr := &Manager{Docker: docker, Logger: testLogger()}

	var first bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &first}); err == nil {
		t.Fatal("first Up: expected the unstartable container to fail the launch")
	}

	var second bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &second})
	if err == nil {
		t.Fatal("second Up: expected the unstartable container to fail the launch")
	}
	chain := errors.CauseChain(err)
	if strings.Contains(chain, "Conflict") || strings.Contains(chain, "already in use") {
		t.Errorf("second Up reported a name conflict instead of the init failure: %s", chain)
	}
	if !strings.Contains(chain, "error during container init") {
		t.Errorf("second Up did not report the init failure, got: %s", chain)
	}
	if !slices.Contains(docker.removeCalls, "held-container-1") {
		t.Errorf("expected the never-started container to be removed, removeCalls = %v", docker.removeCalls)
	}
}

// TestSingleLine_FlattensAndCaps pins the shape the marker formatter needs: the
// reason field is cut at the first newline and a blank line truncates the field
// block, so a Docker error must arrive as one bounded line.
func TestSingleLine_FlattensAndCaps(t *testing.T) {
	if got := singleLine("first line\n\nsecond  line\n"); got != "first line second line" {
		t.Errorf("singleLine = %q, want %q", got, "first line second line")
	}
	long := singleLine(strings.Repeat("ä", 600))
	if n := len([]rune(long)); n != 501 {
		t.Errorf("capped length = %d runes, want 501", n)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("capped value must end in an ellipsis, got %q", long[len(long)-8:])
	}
}
