package devcontainer

import (
	"context"
	"io"
)

// DockerClient abstracts Docker Engine operations for devcontainer management.
// The interface is intentionally simplified compared to the raw Docker SDK;
// the implementation in docker_engine.go handles the SDK-specific details.
type DockerClient interface {
	// Image operations
	ImageBuild(ctx context.Context, buildContext io.Reader, opts ImageBuildOptions) (io.ReadCloser, error)
	ImagePull(ctx context.Context, ref string, opts ImagePullOptions) (io.ReadCloser, error)
	ImageInspect(ctx context.Context, imageRef string) (ImageInspectResponse, error)
	ImageList(ctx context.Context, opts ImageListOptions) ([]ImageSummary, error)

	// Container lifecycle
	ContainerCreate(ctx context.Context, opts ContainerCreateOptions) (string, error)
	ContainerStart(ctx context.Context, containerID string) error
	ContainerStop(ctx context.Context, containerID string, timeout *int) error
	ContainerRemove(ctx context.Context, containerID string, opts ContainerRemoveOptions) error
	ContainerInspect(ctx context.Context, containerID string) (ContainerInspectResponse, error)
	ContainerList(ctx context.Context, opts ContainerListOptions) ([]ContainerSummary, error)
	ContainerLogs(ctx context.Context, containerID string, opts LogsOptions) (io.ReadCloser, error)
	// ContainerCommit commits the container as an image. env entries are baked
	// into the image as ENV so feature-contributed variables (e.g. the node and
	// go features' PATH additions) persist into containers created from the image.
	ContainerCommit(ctx context.Context, containerID string, ref string, env map[string]string) (string, error)

	// File transfer
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader) error
	// CopyFromContainer streams srcPath from the container as a tar archive.
	// The caller closes the returned reader.
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, error)

	// Exec for lifecycle hooks and user commands.
	ExecCreate(ctx context.Context, containerID string, cmd []string, opts ExecOptions) (string, error)
	ExecAttach(ctx context.Context, execID string) (ExecAttachResponse, error)
	ExecInspect(ctx context.Context, execID string) (ExecInspectResponse, error)

	Close() error
}

// --- Wrapper types for the interface ---

// ImageBuildOptions configures an image build.
type ImageBuildOptions struct {
	Dockerfile string
	Tags       []string
	BuildArgs  map[string]*string
	Target     string
	CacheFrom  []string
	Remove     bool // remove intermediate containers
}

// ImagePullOptions configures an image pull.
type ImagePullOptions struct {
	RegistryAuth string // base64-encoded auth for private registries
}

// ImageInspectResponse holds image inspection results.
type ImageInspectResponse struct {
	ID   string
	Tags []string
}

// ImageListOptions filters image listing.
type ImageListOptions struct {
	LabelFilters map[string]string // label key=value pairs
}

// ImageSummary holds basic image metadata.
type ImageSummary struct {
	ID   string
	Tags []string
}

// ContainerCreateOptions bundles all parameters for creating a container.
// The implementation maps these to separate Docker SDK parameters.
type ContainerCreateOptions struct {
	Name        string
	Image       string
	Cmd         []string
	Env         []string // KEY=VALUE pairs
	Labels      map[string]string
	WorkingDir  string
	User        string
	ExtraHosts  []string // "host:ip" entries for /etc/hosts
	Binds       []string // "src:dst[:opts]" bind mounts
	CapAdd      []string // added capabilities
	SecurityOpt []string // security options
	Privileged  bool
	NetworkMode string
	ShmSize     int64
}

// ContainerRemoveOptions configures container removal.
type ContainerRemoveOptions struct {
	Force         bool
	RemoveVolumes bool
}

// ContainerInspectResponse holds container inspection results.
type ContainerInspectResponse struct {
	ID     string
	Name   string
	State  ContainerState
	Image  string
	Config ContainerConfigInfo
}

// ContainerState represents the container's runtime state.
type ContainerState struct {
	Status   string // "running", "exited", "created", etc.
	Running  bool
	ExitCode int
}

// ContainerConfigInfo holds container configuration from inspection.
type ContainerConfigInfo struct {
	Env    []string
	Labels map[string]string
}

// ContainerListOptions filters container listing.
type ContainerListOptions struct {
	All          bool              // include stopped containers
	LabelFilters map[string]string // label key=value pairs
	NameFilter   string            // filter by container name (Docker regex match)
}

// ContainerSummary holds basic container metadata from listing.
type ContainerSummary struct {
	ID     string
	Names  []string
	Image  string
	State  string // "running", "exited", etc.
	Labels map[string]string
}

// LogsOptions configures container log retrieval.
type LogsOptions struct {
	Follow     bool
	Tail       string // number of lines, e.g. "100"
	ShowStdout bool
	ShowStderr bool
}

// ExecOptions configures command execution inside a container.
type ExecOptions struct {
	User         string
	WorkingDir   string
	Env          []string // KEY=VALUE pairs
	AttachStdout bool
	AttachStderr bool
	AttachStdin  bool
	Tty          bool
}

// ExecAttachResponse wraps the exec attachment, providing access to stdout/stderr.
type ExecAttachResponse struct {
	Reader io.Reader
	Conn   io.Closer
}

// Close releases the underlying connection.
func (r ExecAttachResponse) Close() error {
	if r.Conn != nil {
		return r.Conn.Close()
	}
	return nil
}

// ExecInspectResponse holds exec process inspection results.
type ExecInspectResponse struct {
	ExitCode int
	Running  bool
}
