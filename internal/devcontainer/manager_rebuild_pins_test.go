package devcontainer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gethuman-sh/human/errors"
)

// projectConfigHash is the hash the manager derives from a project's
// devcontainer.json — a listing carrying it is "config unchanged", any other
// value is "config changed", and the two take opposite branches.
func projectConfigHash(t *testing.T, projectDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, ".devcontainer", "devcontainer.json"))
	if err != nil {
		t.Fatal(err)
	}
	return ConfigHash(data)
}

// TestManager_Up_RelaunchReportsTheDeadContainersOwnError is the acceptance
// criterion in its decidable form: the rebuild dies of something else than the
// container it replaced, so the chain can only name the original cause if the
// removed container's State.Error was actually carried forward. With one error
// for both starts, the rebuild's own failure satisfies the same assertion and
// carrying nothing forward passes too (SC-4632).
func TestManager_Up_RelaunchReportsTheDeadContainersOwnError(t *testing.T) {
	const deadContainerErr = `error during container init: error mounting "/host/.git" to rootfs: no such file or directory`
	const rebuildErr = "failed to create shim task: no space left on device"

	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := newNameHoldingDocker(deadContainerErr)
	docker.laterInitError = rebuildErr
	mgr := &Manager{Docker: docker, Logger: testLogger()}

	var first bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &first}); err == nil {
		t.Fatal("first Up: expected the unstartable container to fail the launch")
	}

	var second bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &second})
	if err == nil {
		t.Fatal("second Up: expected the rebuild to fail the launch")
	}
	chain := errors.CauseChain(err)
	if !strings.Contains(chain, `error mounting "/host/.git" to rootfs`) {
		t.Errorf("the removed container's own init error is missing from the chain: %s", chain)
	}
	if !strings.Contains(chain, rebuildErr) {
		t.Errorf("the rebuild's own failure is missing from the chain: %s", chain)
	}
	if prior, _ := errors.AllDetails(err)["prior_error"].(string); !strings.Contains(prior, "no such file or directory") {
		t.Errorf("prior_error detail = %q, want the removed container's State.Error", prior)
	}
}

// TestManager_Up_CarriedInitErrorArrivesFlattened pins singleLine at the call
// site rather than in isolation: the daemon cuts a launch failure at the first
// newline to build a *-failed marker, so a multi-line State.Error reaching the
// error chain verbatim loses most of itself on the card (SC-4632).
func TestManager_Up_CarriedInitErrorArrivesFlattened(t *testing.T) {
	const deadContainerErr = "OCI runtime create failed:\n\nunable to start container process:\nexec /bin/sh: exec format error"

	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := newNameHoldingDocker(deadContainerErr)
	docker.laterInitError = "rebuild refused by daemon"
	mgr := &Manager{Docker: docker, Logger: testLogger()}

	var first bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &first}); err == nil {
		t.Fatal("first Up: expected the unstartable container to fail the launch")
	}

	var second bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &second})
	if err == nil {
		t.Fatal("second Up: expected the rebuild to fail the launch")
	}
	chain := errors.CauseChain(err)
	const flattened = "OCI runtime create failed: unable to start container process: exec /bin/sh: exec format error"
	if !strings.Contains(chain, flattened) {
		t.Errorf("carried init error did not arrive flattened, got: %q", chain)
	}
	if strings.Contains(chain, "\n") {
		t.Errorf("a newline in the chain truncates the marker's reason field, got: %q", chain)
	}
}

// TestManager_Up_StaleMetadataOfTheRemovedContainerIsDeleted pins the other
// half of freeing the name: the metadata points at a container that no longer
// exists, and a rebuild that dies before writing its own would otherwise leave
// it behind as the project's current state (SC-4632).
func TestManager_Up_StaleMetadataOfTheRemovedContainerIsDeleted(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := newNameHoldingDocker("error during container init: no such file or directory")
	mgr := &Manager{Docker: docker, Logger: testLogger()}

	var first bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &first}); err == nil {
		t.Fatal("first Up: expected the unstartable container to fail the launch")
	}

	name := SanitizeName(filepath.Base(projectDir))
	if err := WriteMeta(Meta{Name: name, ProjectDir: projectDir, ContainerID: "held-container-1", Status: StatusRunning}); err != nil {
		t.Fatal(err)
	}

	var second bytes.Buffer
	if _, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &second}); err == nil {
		t.Fatal("second Up: expected the rebuild to fail the launch")
	}
	if _, err := os.Stat(MetaPath(name)); !os.IsNotExist(err) {
		meta, _ := ReadMeta(name)
		t.Errorf("metadata of the removed container survived the rebuild: %+v", meta)
	}
}

// TestManager_Up_CreatedListingThatHasRunIsNotTreatedAsNeverStarted pins the
// state filter the removal decision hangs on. The listing is a snapshot saying
// "created"; only the inspect says whether that container is alive or has ever
// run, and either answer disqualifies it from the never-started path. Both
// halves of that condition are load-bearing, so both are asserted separately:
// with only one of them the other case is removed out from under a container
// that is doing its job (SC-4632).
func TestManager_Up_CreatedListingThatHasRunIsNotTreatedAsNeverStarted(t *testing.T) {
	cases := []struct {
		name  string
		state ContainerState
	}{
		{
			// Started since the listing: alive now, StartedAt not yet visible
			// to a caller reading Running alone would be the only signal.
			name:  "running now with no StartedAt",
			state: ContainerState{Status: "running", Running: true},
		},
		{
			// Ran and stopped since the listing: not running any more, but it
			// has a StartedAt, so it is not a container that never started.
			name:  "not running but has run",
			state: ContainerState{Status: "exited", StartedAt: time.Now().Add(-time.Minute)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
			containerName := ContainerName(projectDir)
			hash := projectConfigHash(t, projectDir)

			docker := &staleListDocker{
				mockDockerClient: &mockDockerClient{
					imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
					createID:           "container-abc123",
				},
				running:      tc.state.Running,
				inspectState: tc.state,
				summary: ContainerSummary{
					ID:     "listed-created-id",
					Names:  []string{"/" + containerName},
					Image:  "ubuntu:22.04",
					State:  "created",
					Labels: map[string]string{LabelManaged: "true", LabelConfigHash: hash},
				},
			}

			mgr := &Manager{Docker: docker, Logger: testLogger()}
			var buf bytes.Buffer
			_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
			if err != nil {
				t.Fatalf("Up failed on a container it should have reused: %s", errors.CauseChain(err))
			}
			if len(docker.removeCalls) != 0 {
				t.Errorf("a container that has run was removed, removeCalls = %v", docker.removeCalls)
			}
			if docker.removed {
				t.Error("the container was removed although it had run")
			}
			if len(docker.createCalls) != 0 {
				t.Errorf("must reuse the existing container, createCalls = %d", len(docker.createCalls))
			}
		})
	}
}

// TestManager_Up_ConfigChangedRemovalTakesTheContainerRunningOrNot pins the
// other side of the sparing rule: a container the config no longer matches
// cannot serve the run whatever its state, so its removal is forced. Sparing
// here does not protect anything — it strands the launch on a name held by a
// container that is useless to it (SC-4632).
func TestManager_Up_ConfigChangedRemovalTakesTheContainerRunningOrNot(t *testing.T) {
	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	containerName := ContainerName(projectDir)

	docker := &staleListDocker{
		mockDockerClient: &mockDockerClient{
			imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
			createID:           "container-abc123",
		},
		running:      true,
		inspectState: ContainerState{Status: "running", Running: true},
		summary: ContainerSummary{
			ID:     "outdated-id",
			Names:  []string{"/" + containerName},
			Image:  "ubuntu:22.04",
			State:  "running",
			Labels: map[string]string{LabelManaged: "true", LabelConfigHash: "hash-of-an-older-devcontainer-json"},
		},
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	meta, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err != nil {
		t.Fatalf("Up failed although the outdated container was replaceable: %s", errors.CauseChain(err))
	}
	if len(docker.removeOpts) != 1 || !docker.removeOpts[0].Force {
		t.Errorf("the outdated container must be removed with force, removeOpts = %+v", docker.removeOpts)
	}
	meta = must(t, meta, "expected non-nil meta")
	if meta.ContainerID != "container-abc123" {
		t.Errorf("containerID = %q, want the freshly created one", meta.ContainerID)
	}
}

// vanishedNeverStartedDocker models the compound case: the container is listed
// "created", inspect still answers with what killed it, and by the time the
// removal runs something else has taken it. The rebuild then dies of its own
// cause. removeErr is Docker's answer at the moment of removal.
type vanishedNeverStartedDocker struct {
	*mockDockerClient
	summary      ContainerSummary
	inspectState ContainerState
	removeErr    error
	startErrMsg  string
}

func (d *vanishedNeverStartedDocker) ContainerList(_ context.Context, _ ContainerListOptions) ([]ContainerSummary, error) {
	return []ContainerSummary{d.summary}, nil
}

func (d *vanishedNeverStartedDocker) ContainerInspect(_ context.Context, id string) (ContainerInspectResponse, error) {
	return ContainerInspectResponse{ID: id, State: d.inspectState}, nil
}

func (d *vanishedNeverStartedDocker) ContainerRemove(ctx context.Context, id string, opts ContainerRemoveOptions) error {
	_ = d.mockDockerClient.ContainerRemove(ctx, id, opts)
	return d.removeErr
}

func (d *vanishedNeverStartedDocker) ContainerStart(ctx context.Context, id string) error {
	_ = d.mockDockerClient.ContainerStart(ctx, id)
	return fmt.Errorf("Error response from daemon: %s", d.startErrMsg)
}

// TestManager_Up_VanishedNeverStartedContainerStillReportsItsInitError is the
// two races at once: the container that never started is gone before the
// removal can take it, and the rebuild dies too. Tolerating the already-gone
// removal is what keeps the launch on the path that carries the init error —
// treating it as a failure loses the diagnosis and blames the removal instead
// (SC-4632).
func TestManager_Up_VanishedNeverStartedContainerStillReportsItsInitError(t *testing.T) {
	const deadContainerErr = `error during container init: error mounting "/host/cache" to rootfs: no such file or directory`
	const rebuildErr = "failed to create shim task: no space left on device"

	projectDir, _, _ := setupTestProject(t, `{"image": "ubuntu:22.04"}`)
	docker := &vanishedNeverStartedDocker{
		mockDockerClient: &mockDockerClient{
			imageInspectResult: ImageInspectResponse{ID: "sha256:cached"},
			createID:           "container-abc123",
		},
		inspectState: ContainerState{Status: "created", ExitCode: 128, Error: deadContainerErr},
		removeErr:    dockerNotFound("gone-id"),
		startErrMsg:  rebuildErr,
		summary: ContainerSummary{
			ID:     "gone-id",
			Names:  []string{"/" + ContainerName(projectDir)},
			Image:  "ubuntu:22.04",
			State:  "created",
			Labels: map[string]string{LabelManaged: "true"},
		},
	}

	mgr := &Manager{Docker: docker, Logger: testLogger()}
	var buf bytes.Buffer
	_, err := mgr.Up(context.Background(), UpOptions{ProjectDir: projectDir, Out: &buf})
	if err == nil {
		t.Fatal("expected the failing rebuild to fail the launch")
	}
	if len(docker.createCalls) != 1 {
		t.Fatalf("a container Docker says is gone frees its name, createCalls = %d", len(docker.createCalls))
	}
	chain := errors.CauseChain(err)
	if !strings.Contains(chain, `error mounting "/host/cache" to rootfs`) {
		t.Errorf("the vanished container's init error is missing from the chain: %s", chain)
	}
	if !strings.Contains(chain, rebuildErr) {
		t.Errorf("the rebuild's own failure is missing from the chain: %s", chain)
	}
}
