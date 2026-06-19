package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/gethuman-sh/human/internal/dockerhost"
)

// NewEngineDockerClient creates a DockerClient backed by the Docker Engine API.
//
// When DOCKER_HOST is unset, it resolves the active docker CLI context's
// endpoint (colima/OrbStack/Rancher/Docker-Desktop/Podman) so the TUI's
// container discovery and `human usage` reach the engine out-of-the-box. The
// resolution is shared with devcontainer.NewDockerClient via
// internal/dockerhost so the two never diverge.
func NewEngineDockerClient() (DockerClient, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host := dockerhost.Resolve().Host; host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}
	return &engineDockerClient{cli: cli}, nil
}

type engineDockerClient struct {
	cli *client.Client
}

func (e *engineDockerClient) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	list, err := e.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, err
	}
	infos := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			// Docker container names start with "/".
			name = c.Names[0]
			if len(name) > 0 && name[0] == '/' {
				name = name[1:]
			}
		}
		infos = append(infos, ContainerInfo{ID: c.ID, Name: name, Labels: c.Labels})
	}
	return infos, nil
}

func (e *engineDockerClient) Exec(ctx context.Context, containerID string, cmd []string) (int, io.Reader, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}
	resp, err := e.cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return 0, nil, err
	}

	attach, err := e.cli.ContainerExecAttach(ctx, resp.ID, container.ExecStartOptions{})
	if err != nil {
		return 0, nil, err
	}
	// Defer Close so every return path releases the connection — a
	// future docker SDK revision that returns an error from Close
	// here cannot then silently drop it under a dual-close pattern.
	defer attach.Close()

	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, attach.Reader); err != nil {
		return 0, nil, err
	}

	inspect, err := e.cli.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return 0, nil, err
	}

	return inspect.ExitCode, &stdout, nil
}

func (e *engineDockerClient) ContainerStats(ctx context.Context, containerID string) (*MemoryInfo, error) {
	resp, err := e.cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}
	return &MemoryInfo{
		Usage: stats.MemoryStats.Usage,
		Limit: stats.MemoryStats.Limit,
	}, nil
}

func (e *engineDockerClient) Close() error {
	return e.cli.Close()
}

// Verify interface compliance.
var _ DockerClient = (*engineDockerClient)(nil)
