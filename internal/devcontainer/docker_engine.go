package devcontainer

import (
	"context"
	"io"
	"sort"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// NewDockerClient creates a DockerClient backed by the Docker Engine API.
//
// When DOCKER_HOST is unset, it resolves the active docker CLI context's
// endpoint (colima/OrbStack/Rancher/Docker-Desktop/Podman) so human reaches the
// engine out-of-the-box, mirroring what the docker CLI does. Explicit
// DOCKER_HOST / DOCKER_CONTEXT always win. The resolution is shared with
// claude.NewEngineDockerClient via internal/dockerhost so the two never diverge.
func NewDockerClient() (DockerClient, error) {
	// newRawDockerClient (host.go) applies the shared dockerhost.Resolve()
	// endpoint resolution; explicit DOCKER_HOST / DOCKER_CONTEXT still win.
	cli, err := newRawDockerClient()
	if err != nil {
		return nil, err
	}
	return &engineClient{cli: cli}, nil
}

type engineClient struct {
	cli *client.Client
}

// labelFilters builds the moby client's filter predicate from our label map.
func labelFilters(labels map[string]string) client.Filters {
	f := client.Filters{}
	if len(labels) == 0 {
		return f
	}
	terms := make(map[string]bool, len(labels))
	for k, v := range labels {
		terms[k+"="+v] = true
	}
	f["label"] = terms
	return f
}

func (e *engineClient) ImageBuild(ctx context.Context, buildContext io.Reader, opts ImageBuildOptions) (io.ReadCloser, error) {
	sdkOpts := client.ImageBuildOptions{
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
	resp, err := e.cli.ImagePull(ctx, ref, client.ImagePullOptions{
		RegistryAuth: opts.RegistryAuth,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
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
	list, err := e.cli.ImageList(ctx, client.ImageListOptions{Filters: labelFilters(opts.LabelFilters)})
	if err != nil {
		return nil, err
	}
	summaries := make([]ImageSummary, 0, len(list.Items))
	for _, img := range list.Items {
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
		Cmd:        opts.Cmd,
		Env:        opts.Env,
		Labels:     opts.Labels,
		WorkingDir: opts.WorkingDir,
		User:       opts.User,
	}

	hostConfig := &container.HostConfig{
		Binds:       opts.Binds,
		ExtraHosts:  opts.ExtraHosts,
		CapAdd:      opts.CapAdd,
		SecurityOpt: opts.SecurityOpt,
		Privileged:  opts.Privileged,
		ShmSize:     opts.ShmSize,
	}
	if opts.NetworkMode != "" {
		hostConfig.NetworkMode = container.NetworkMode(opts.NetworkMode)
	}

	resp, err := e.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
		Name:       opts.Name,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (e *engineClient) ContainerStart(ctx context.Context, containerID string) error {
	_, err := e.cli.ContainerStart(ctx, containerID, client.ContainerStartOptions{})
	return err
}

func (e *engineClient) ContainerStop(ctx context.Context, containerID string, timeout *int) error {
	_, err := e.cli.ContainerStop(ctx, containerID, client.ContainerStopOptions{Timeout: timeout})
	return err
}

func (e *engineClient) ContainerRemove(ctx context.Context, containerID string, opts ContainerRemoveOptions) error {
	_, err := e.cli.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{
		Force:         opts.Force,
		RemoveVolumes: opts.RemoveVolumes,
	})
	return err
}

func (e *engineClient) ContainerInspect(ctx context.Context, containerID string) (ContainerInspectResponse, error) {
	resp, err := e.cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInspectResponse{}, err
	}
	ctr := resp.Container
	state := ContainerState{}
	if ctr.State != nil {
		state.Status = string(ctr.State.Status)
		state.Running = ctr.State.Running
		state.ExitCode = ctr.State.ExitCode
	}
	configInfo := ContainerConfigInfo{}
	if ctr.Config != nil {
		configInfo.Env = ctr.Config.Env
		configInfo.Labels = ctr.Config.Labels
	}
	return ContainerInspectResponse{
		ID:     ctr.ID,
		Name:   ctr.Name,
		State:  state,
		Image:  ctr.Image,
		Config: configInfo,
	}, nil
}

func (e *engineClient) ContainerList(ctx context.Context, opts ContainerListOptions) ([]ContainerSummary, error) {
	f := labelFilters(opts.LabelFilters)
	if opts.NameFilter != "" {
		f["name"] = map[string]bool{opts.NameFilter: true}
	}
	list, err := e.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     opts.All,
		Filters: f,
	})
	if err != nil {
		return nil, err
	}
	summaries := make([]ContainerSummary, 0, len(list.Items))
	for _, c := range list.Items {
		summaries = append(summaries, ContainerSummary{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			State:  string(c.State),
			Labels: c.Labels,
		})
	}
	return summaries, nil
}

func (e *engineClient) ContainerLogs(ctx context.Context, containerID string, opts LogsOptions) (io.ReadCloser, error) {
	resp, err := e.cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		Follow:     opts.Follow,
		Tail:       opts.Tail,
		ShowStdout: opts.ShowStdout,
		ShowStderr: opts.ShowStderr,
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (e *engineClient) ContainerCommit(ctx context.Context, containerID string, ref string, env map[string]string) (string, error) {
	// Bake feature-contributed env into the image as ENV directives. Sorted for
	// a deterministic image (stable layer regardless of map iteration order).
	changes := make([]string, 0, len(env))
	for k, v := range env {
		changes = append(changes, "ENV "+k+"="+v)
	}
	sort.Strings(changes)

	resp, err := e.cli.ContainerCommit(ctx, containerID, client.ContainerCommitOptions{
		Reference: ref,
		Changes:   changes,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (e *engineClient) CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error {
	_, err := e.cli.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{
		DestinationPath: dstPath,
		Content:         content,
	})
	return err
}

func (e *engineClient) CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error) {
	// The SDK returns the archive reader plus a PathStat; only the archive is
	// needed for transcript copy-out.
	resp, err := e.cli.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{
		SourcePath: srcPath,
	})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

func (e *engineClient) ExecCreate(ctx context.Context, containerID string, cmd []string, opts ExecOptions) (string, error) {
	resp, err := e.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		User:         opts.User,
		WorkingDir:   opts.WorkingDir,
		Env:          opts.Env,
		Cmd:          cmd,
		AttachStdout: opts.AttachStdout,
		AttachStderr: opts.AttachStderr,
		AttachStdin:  opts.AttachStdin,
		TTY:          opts.Tty,
	})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (e *engineClient) ExecAttach(ctx context.Context, execID string) (ExecAttachResponse, error) {
	attach, err := e.cli.ExecAttach(ctx, execID, client.ExecAttachOptions{})
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
	resp, err := e.cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
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
