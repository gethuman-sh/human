package devcontainer

import (
	"context"
	"io"
	"sort"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/gethuman-sh/human/internal/dockerhost"
)

// NewDockerClient creates a DockerClient backed by the Docker Engine API.
//
// When DOCKER_HOST is unset, it resolves the active docker CLI context's
// endpoint (colima/OrbStack/Rancher/Docker-Desktop/Podman) so human reaches the
// engine out-of-the-box, mirroring what the docker CLI does. Explicit
// DOCKER_HOST / DOCKER_CONTEXT always win. The resolution is shared with
// claude.NewEngineDockerClient via internal/dockerhost so the two never diverge.
func NewDockerClient() (DockerClient, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	// A non-empty host means the active context resolved to a concrete
	// endpoint; layer it on top of FromEnv. Empty means "use env/platform
	// default", so we leave FromEnv to do its job.
	if host := dockerhost.Resolve().Host; host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}

type engineClient struct {
	cli *client.Client
}

func (e *engineClient) ImageBuild(ctx context.Context, buildContext io.Reader, opts ImageBuildOptions) (io.ReadCloser, error) {
	sdkOpts := build.ImageBuildOptions{
		Dockerfile: opts.Dockerfile,
		Tags:       opts.Tags,
		BuildArgs:  opts.BuildArgs,
		Target:     opts.Target,
		CacheFrom:  opts.CacheFrom,
		Remove:     opts.Remove,
	}
	resp, err := e.cli.ImageBuild(ctx, buildContext, sdkOpts)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (e *engineClient) ImagePull(ctx context.Context, ref string, opts ImagePullOptions) (io.ReadCloser, error) {
	sdkOpts := dockerimage.PullOptions{
		RegistryAuth: opts.RegistryAuth,
	}
	return e.cli.ImagePull(ctx, ref, sdkOpts)
}

func (e *engineClient) ImageInspect(ctx context.Context, imageRef string) (ImageInspectResponse, error) {
	resp, err := e.cli.ImageInspect(ctx, imageRef)
	if err != nil {
		return ImageInspectResponse{}, err
	}
	return ImageInspectResponse{
		ID:   resp.ID,
		Tags: resp.RepoTags,
	}, nil
}

func (e *engineClient) ImageList(ctx context.Context, opts ImageListOptions) ([]ImageSummary, error) {
	f := filters.NewArgs()
	for k, v := range opts.LabelFilters {
		f.Add("label", k+"="+v)
	}
	list, err := e.cli.ImageList(ctx, dockerimage.ListOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	summaries := make([]ImageSummary, 0, len(list))
	for _, img := range list {
		summaries = append(summaries, ImageSummary{
			ID:   img.ID,
			Tags: img.RepoTags,
		})
	}
	return summaries, nil
}

func (e *engineClient) ContainerCreate(ctx context.Context, opts ContainerCreateOptions) (string, error) {
	config := &container.Config{
		Image:      opts.Image,
		Cmd:        strslice.StrSlice(opts.Cmd),
		Env:        opts.Env,
		Labels:     opts.Labels,
		WorkingDir: opts.WorkingDir,
		User:       opts.User,
	}

	hostConfig := &container.HostConfig{
		Binds:       opts.Binds,
		ExtraHosts:  opts.ExtraHosts,
		CapAdd:      strslice.StrSlice(opts.CapAdd),
		SecurityOpt: opts.SecurityOpt,
		Privileged:  opts.Privileged,
		ShmSize:     opts.ShmSize,
	}
	if opts.NetworkMode != "" {
		hostConfig.NetworkMode = container.NetworkMode(opts.NetworkMode)
	}

	resp, err := e.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (e *engineClient) ContainerStart(ctx context.Context, containerID string) error {
	return e.cli.ContainerStart(ctx, containerID, container.StartOptions{})
}

func (e *engineClient) ContainerStop(ctx context.Context, containerID string, timeout *int) error {
	return e.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: timeout})
}

func (e *engineClient) ContainerRemove(ctx context.Context, containerID string, opts ContainerRemoveOptions) error {
	return e.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force:         opts.Force,
		RemoveVolumes: opts.RemoveVolumes,
	})
}

func (e *engineClient) ContainerInspect(ctx context.Context, containerID string) (ContainerInspectResponse, error) {
	resp, err := e.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return ContainerInspectResponse{}, err
	}
	state := ContainerState{}
	if resp.State != nil {
		state.Status = resp.State.Status
		state.Running = resp.State.Running
		state.ExitCode = resp.State.ExitCode
	}
	configInfo := ContainerConfigInfo{}
	if resp.Config != nil {
		configInfo.Env = resp.Config.Env
		configInfo.Labels = resp.Config.Labels
	}
	return ContainerInspectResponse{
		ID:     resp.ID,
		Name:   resp.Name,
		State:  state,
		Image:  resp.Image,
		Config: configInfo,
	}, nil
}

func (e *engineClient) ContainerList(ctx context.Context, opts ContainerListOptions) ([]ContainerSummary, error) {
	f := filters.NewArgs()
	for k, v := range opts.LabelFilters {
		f.Add("label", k+"="+v)
	}
	if opts.NameFilter != "" {
		f.Add("name", opts.NameFilter)
	}
	list, err := e.cli.ContainerList(ctx, container.ListOptions{
		All:     opts.All,
		Filters: f,
	})
	if err != nil {
		return nil, err
	}
	summaries := make([]ContainerSummary, 0, len(list))
	for _, c := range list {
		summaries = append(summaries, ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			State:  c.State,
			Labels: c.Labels,
		})
	}
	return summaries, nil
}

func (e *engineClient) ContainerLogs(ctx context.Context, containerID string, opts LogsOptions) (io.ReadCloser, error) {
	return e.cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		Follow:     opts.Follow,
		Tail:       opts.Tail,
		ShowStdout: opts.ShowStdout,
		ShowStderr: opts.ShowStderr,
	})
}

func (e *engineClient) ContainerCommit(ctx context.Context, containerID string, ref string, env map[string]string) (string, error) {
	// Bake feature-contributed env into the image as ENV directives. Sorted for
	// a deterministic image (stable layer regardless of map iteration order).
	changes := make([]string, 0, len(env))
	for k, v := range env {
		changes = append(changes, "ENV "+k+"="+v)
	}
	sort.Strings(changes)

	resp, err := e.cli.ContainerCommit(ctx, containerID, container.CommitOptions{
		Reference: ref,
		Changes:   changes,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (e *engineClient) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	return e.cli.CopyToContainer(ctx, containerID, dstPath, content, container.CopyToContainerOptions{})
}

func (e *engineClient) ExecCreate(ctx context.Context, containerID string, cmd []string, opts ExecOptions) (string, error) {
	resp, err := e.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		User:         opts.User,
		WorkingDir:   opts.WorkingDir,
		Env:          opts.Env,
		Cmd:          cmd,
		AttachStdout: opts.AttachStdout,
		AttachStderr: opts.AttachStderr,
		AttachStdin:  opts.AttachStdin,
		Tty:          opts.Tty,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (e *engineClient) ExecAttach(ctx context.Context, execID string) (ExecAttachResponse, error) {
	attach, err := e.cli.ContainerExecAttach(ctx, execID, container.ExecStartOptions{})
	if err != nil {
		return ExecAttachResponse{}, err
	}
	// The HijackedResponse.Reader contains multiplexed stdout/stderr.
	// Callers must use stdcopy.StdCopy to demux for non-TTY execs.
	return ExecAttachResponse{
		Reader: attach.Reader,
		Conn:   attach.Conn,
	}, nil
}

func (e *engineClient) ExecInspect(ctx context.Context, execID string) (ExecInspectResponse, error) {
	resp, err := e.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return ExecInspectResponse{}, err
	}
	return ExecInspectResponse{
		ExitCode: resp.ExitCode,
		Running:  resp.Running,
	}, nil
}

func (e *engineClient) Close() error {
	return e.cli.Close()
}

// Verify interface compliance.
var _ DockerClient = (*engineClient)(nil)

// StdCopy re-exports stdcopy.StdCopy so callers within the devcontainer
// package can demux exec output without importing the Docker SDK directly.
var StdCopy = stdcopy.StdCopy
