package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	cerrdefs "github.com/containerd/errdefs"

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

// staleListDocker models the race the "never started" path must survive: the
// list state is a snapshot, so a container listed as "created" may have started
// since, and the inspect that would say so can fail for reasons of its own — a
// timeout, a daemon hiccup. Removal is the only thing that knows the truth at
// the moment it acts, so this fake enforces Docker's rule: a running container
// does not come off without Force.
type staleListDocker struct {
	*mockDockerClient
	mu          sync.Mutex
	running     bool
	removed     bool
	removeOpts  []ContainerRemoveOptions
	summary     ContainerSummary
	inspectFail error
}

func (d *staleListDocker) ContainerInspect(_ context.Context, _ string) (ContainerInspectResponse, error) {
	return ContainerInspectResponse{}, d.inspectFail
}

func (d *staleListDocker) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.removed {
		return nil, nil
	}
	return []ContainerSummary{d.summary}, nil
}

func (d *staleListDocker) ContainerRemove(ctx context.Context, id string, opts ContainerRemoveOptions) error {
	_ = d.mockDockerClient.ContainerRemove(ctx, id, opts)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removeOpts = append(d.removeOpts, opts)
	if d.running && !opts.Force {
		return fmt.Errorf("Error response from daemon: cannot remove container %q: "+
			"container is running: stop the container before removing or force remove", id)
	}
	d.removed = true
	return nil
}

// TestManager_Up_StaleCreatedListingWithFailedInspectDoesNotKillARunningContainer
// pins the SC-4632 acceptance criterion its first fix left open: a container in
// state "running" is never removed by this path. The listing says "created" and
// the inspect fails, so nothing the manager can read proves the container is
// dead — and an unforced removal is what makes the difference decidable by
// Docker, atomically, instead of guessed from a stale snapshot.
func TestManager_Up_StaleCreatedListingWithFailedInspectDoesNotKillARunningContainer(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	containerName := ContainerName(projectDir)

	docker := &staleListDocker{
		mockDockerClient: &mockDockerClient{
			imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
			createID:           "container-abc123",
		},
		running:     true,
		inspectFail: fmt.Errorf("Error response from daemon: i/o timeout"),
		summary: ContainerSummary{
			ID:     "raced-id",
			Names:  []string{"/" + containerName},
			Image:  "ubuntu:22.04",
			State:  "created",
			Labels: map[string]string{LabelManaged: "true"},
		},
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})

	for _, opts := range docker.removeOpts {
		if opts.Force {
			t.Errorf("a container that may be running was force-removed, removeOpts = %+v", docker.removeOpts)
		}
	}
	if docker.removed {
		t.Error("the running container was removed")
	}
	if err == nil {
		t.Fatal("expected the failed removal to fail the launch")
	}
	if chain := errors.CauseChain(err); !strings.Contains(chain, "container is running") {
		t.Errorf("expected the removal refusal in the chain, got: %s", chain)
	}
	if len(docker.createCalls) != 0 {
		t.Errorf("must not create over a container that still holds the name, createCalls = %d", len(docker.createCalls))
	}
}

// vanishedContainerDocker models the window between the listing and the
// removal: the container is in the list, and by the time the manager acts on
// it something else has taken it — the reaper sweep, a concurrent daemon pass,
// a manual `docker rm`. removeErr is what Docker answers at that moment.
type vanishedContainerDocker struct {
	*mockDockerClient
	summary    ContainerSummary
	inspectErr error
	removeErr  error
}

func (d *vanishedContainerDocker) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
	return []ContainerSummary{d.summary}, nil
}

func (d *vanishedContainerDocker) ContainerInspect(_ context.Context, _ string) (ContainerInspectResponse, error) {
	return ContainerInspectResponse{}, d.inspectErr
}

func (d *vanishedContainerDocker) ContainerRemove(ctx context.Context, id string, opts ContainerRemoveOptions) error {
	_ = d.mockDockerClient.ContainerRemove(ctx, id, opts)
	return d.removeErr
}

// dockerNotFound builds the error the Engine SDK returns for a container that
// is no longer there: the message is decoration, the errdefs classification is
// the fact.
func dockerNotFound(id string) error {
	return fmt.Errorf("Error response from daemon: No such container: %s: %w", id, cerrdefs.ErrNotFound)
}

func vanishedDockerFor(containerName string, removeErr error) *vanishedContainerDocker {
	return &vanishedContainerDocker{
		mockDockerClient: &mockDockerClient{
			imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
			createID:           "container-abc123",
		},
		inspectErr: dockerNotFound("gone-id"),
		removeErr:  removeErr,
		summary: ContainerSummary{
			ID:     "gone-id",
			Names:  []string{"/" + containerName},
			Image:  "ubuntu:22.04",
			State:  "created",
			Labels: map[string]string{LabelManaged: "true"},
		},
	}
}

// TestManager_Up_ContainerGoneBeforeRemovalStillLaunches pins the self-heal the
// name-conflict gate must not cost: a container that is already gone frees the
// name exactly as a successful removal does, so the launch proceeds to create
// instead of failing and charging a stage retry (SC-4632).
func TestManager_Up_ContainerGoneBeforeRemovalStillLaunches(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := vanishedDockerFor(ContainerName(projectDir), dockerNotFound("gone-id"))

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err != nil {
		t.Fatalf("Up failed although the name was free: %s", errors.CauseChain(err))
	}
	if len(docker.createCalls) != 1 {
		t.Errorf("expected the launch to create a fresh container, createCalls = %d", len(docker.createCalls))
	}
	meta = must(t, meta, "expected non-nil meta")
	if meta.ContainerID != "container-abc123" {
		t.Errorf("containerID = %q, want the freshly created one", meta.ContainerID)
	}
}

// TestManager_Up_RemovalFailingForAnotherReasonStopsTheLaunch keeps the gate
// itself: only "already gone" frees the name. Any other refusal leaves the old
// container holding it, and creating over it returns Docker's name conflict as
// the run's only visible failure (SC-4632).
func TestManager_Up_RemovalFailingForAnotherReasonStopsTheLaunch(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := vanishedDockerFor(ContainerName(projectDir),
		fmt.Errorf("Error response from daemon: permission denied while removing container"))

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err == nil {
		t.Fatal("expected the failed removal to fail the launch")
	}
	if chain := errors.CauseChain(err); !strings.Contains(chain, "permission denied") {
		t.Errorf("expected the removal failure in the chain, got: %s", chain)
	}
	if len(docker.createCalls) != 0 {
		t.Errorf("must not create over a name still held, createCalls = %d", len(docker.createCalls))
	}
}
