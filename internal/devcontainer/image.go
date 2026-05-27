package devcontainer

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// ImageBuilder handles building or pulling devcontainer images.
type ImageBuilder struct {
	Docker DockerClient
	Logger zerolog.Logger
}

// EnsureImage ensures a devcontainer image exists. If not cached (or rebuild
// requested), it builds/pulls the image per the devcontainer config.
// Returns the image ID.
func (b *ImageBuilder) EnsureImage(ctx context.Context, cfg *DevcontainerConfig, projectDir, configHash string, rebuild bool, out io.Writer) (string, string, error) {
	imageName := ImageName(projectDir, configHash)

	// Check cache: a committed image with features already baked in.
	if !rebuild {
		if resp, err := b.Docker.ImageInspect(ctx, imageName); err == nil {
			_, _ = fmt.Fprintf(out, "Using cached image %s\n", imageName) // #nosec G705 -- CLI output
			return resp.ID, imageName, nil
		}
	}

	// Pull or build the base image.
	var baseRef string
	switch {
	case cfg.Build != nil && cfg.Build.Dockerfile != "":
		id, name, err := b.buildFromDockerfile(ctx, cfg, projectDir, imageName, out)
		if err != nil {
			return "", "", err
		}
		if len(cfg.Features) == 0 {
			return id, name, nil
		}
		baseRef = name
	case cfg.DockerFile != "":
		build := &BuildConfig{Dockerfile: cfg.DockerFile}
		id, name, err := b.buildFromDockerfile(ctx, &DevcontainerConfig{Build: build}, projectDir, imageName, out)
		if err != nil {
			return "", "", err
		}
		if len(cfg.Features) == 0 {
			return id, name, nil
		}
		baseRef = name
	case cfg.Image != "":
		id, ref, err := b.pullImage(ctx, cfg.Image, imageName, out)
		if err != nil {
			return "", "", err
		}
		if len(cfg.Features) == 0 {
			return id, ref, nil
		}
		baseRef = ref
	default:
		return "", "", errors.WithDetails("devcontainer.json must specify image or build.dockerfile")
	}

	// Features present: install in a temp container and commit as the cached image.
	return b.buildWithFeatures(ctx, cfg, baseRef, imageName, out)
}

// buildWithFeatures creates a temp container, installs features, and commits
// the result as the cached image. This ensures subsequent container creations
// from this image skip feature installation entirely.
func (b *ImageBuilder) buildWithFeatures(ctx context.Context, cfg *DevcontainerConfig, baseRef, imageName string, out io.Writer) (string, string, error) {
	remoteUser := cfg.RemoteUser
	if remoteUser == "" {
		remoteUser = "root"
	}

	_, _ = fmt.Fprintln(out, "Installing features into image (this is cached for future use)...")

	// Create a temp container from the base image.
	tempName := "human-dc-build-" + fmt.Sprintf("%d", time.Now().UnixNano())
	tempID, err := b.Docker.ContainerCreate(ctx, ContainerCreateOptions{
		Name:  tempName,
		Image: baseRef,
		Cmd:   []string{"sleep", "infinity"},
	})
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "creating temp container for features")
	}
	defer func() {
		_ = b.Docker.ContainerRemove(ctx, tempID, ContainerRemoveOptions{Force: true})
	}()

	if err := b.Docker.ContainerStart(ctx, tempID); err != nil {
		return "", "", errors.WrapWithDetails(err, "starting temp container")
	}

	// Install features.
	puller := &OCIFeaturePuller{}
	if err := InstallFeatures(ctx, b.Docker, puller, tempID, cfg.Features, remoteUser, b.Logger, out); err != nil {
		return "", "", errors.WrapWithDetails(err, "installing features")
	}

	// Commit the container as the cached image.
	committedID, err := b.Docker.ContainerCommit(ctx, tempID, imageName)
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "committing image with features")
	}

	_, _ = fmt.Fprintf(out, "Image cached: %s\n", imageName) // #nosec G705 -- CLI output
	return committedID, imageName, nil
}

// pullImage pulls a base image. The container is created using the original
// image ref directly (no re-tagging needed for image-only configs).
func (b *ImageBuilder) pullImage(ctx context.Context, ref, targetName string, out io.Writer) (string, string, error) {
	_, _ = fmt.Fprintf(out, "Pulling %s...\n", ref) // #nosec G705 -- CLI output
	reader, err := b.Docker.ImagePull(ctx, ref, ImagePullOptions{})
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "pulling image", "ref", ref)
	}
	defer func() { _ = reader.Close() }()

	// Drain pull output, capturing any error messages.
	if pullErr := drainDockerOutput(reader); pullErr != nil {
		return "", "", errors.WrapWithDetails(pullErr, "image pull failed", "ref", ref)
	}

	resp, err := b.Docker.ImageInspect(ctx, ref)
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "inspecting pulled image", "ref", ref)
	}

	_, _ = fmt.Fprintf(out, "Image ready: %s\n", ref) // #nosec G705 -- CLI output
	// Use the original ref as imageName so ContainerCreate can find it.
	return resp.ID, ref, nil
}

// buildFromDockerfile builds an image from a Dockerfile.
func (b *ImageBuilder) buildFromDockerfile(ctx context.Context, cfg *DevcontainerConfig, projectDir, imageName string, out io.Writer) (string, string, error) {
	dockerfile := cfg.Build.Dockerfile

	// Resolve build context directory.
	contextDir := projectDir
	if cfg.Build.Context != "" {
		contextDir = filepath.Join(filepath.Dir(filepath.Join(projectDir, ".devcontainer", dockerfile)), cfg.Build.Context)
	}

	_, _ = fmt.Fprintf(out, "Building image from %s (context: %s)...\n", dockerfile, contextDir) // #nosec G705 -- CLI output

	// Create tar archive of the build context.
	buildCtx, err := createBuildContext(contextDir, filepath.Join(projectDir, ".devcontainer", dockerfile))
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "creating build context", "dir", contextDir)
	}

	// Convert build args.
	buildArgs := make(map[string]*string)
	for k, v := range cfg.Build.Args {
		v := v
		buildArgs[k] = &v
	}

	reader, err := b.Docker.ImageBuild(ctx, buildCtx, ImageBuildOptions{
		Dockerfile: filepath.Base(dockerfile),
		Tags:       []string{imageName},
		BuildArgs:  buildArgs,
		Target:     cfg.Build.Target,
		CacheFrom:  cfg.Build.CacheFrom,
		Remove:     true,
	})
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "building image")
	}
	defer func() { _ = reader.Close() }()

	// Drain build output, capturing any error messages.
	if buildErr := drainDockerOutput(reader); buildErr != nil {
		return "", "", errors.WrapWithDetails(buildErr, "image build failed")
	}

	resp, err := b.Docker.ImageInspect(ctx, imageName)
	if err != nil {
		return "", "", errors.WrapWithDetails(err, "inspecting built image")
	}

	_, _ = fmt.Fprintf(out, "Image built: %s\n", imageName)
	return resp.ID, imageName, nil
}

// dockerMessage is a line from Docker build/pull JSON output stream.
type dockerMessage struct {
	Error string `json:"error"`
}

// drainDockerOutput reads Docker JSON stream output to completion, returning
// the first error message found. Docker build and pull APIs embed errors in
// the JSON stream rather than the HTTP status.
func drainDockerOutput(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var msg dockerMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			// Drain the rest to avoid connection reset.
			_, _ = io.Copy(io.Discard, r)
			return errors.WithDetails(msg.Error)
		}
	}
	return nil
}

// createBuildContext creates a tar archive from a directory, suitable for
// Docker image build. If dockerfilePath is outside the context dir, it is
// added to the tar as "Dockerfile".
func createBuildContext(contextDir, dockerfilePath string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	defer func() { _ = tw.Close() }()

	absContext, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}

	if err := tarDirectory(tw, absContext); err != nil {
		return nil, err
	}

	if err := addExternalDockerfile(tw, dockerfilePath, absContext); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// skippedDirs contains directory names excluded from build contexts.
var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, ".devcontainer": true,
}

// tarDirectory walks a directory and adds all files to the tar writer.
func tarDirectory(tw *tar.Writer, absContext string) error {
	return filepath.Walk(absContext, func(path string, info os.FileInfo, err error) error { // #nosec G703 -- absContext is from filepath.Abs
		if err != nil {
			return err
		}
		if info.IsDir() && skippedDirs[filepath.Base(path)] {
			return filepath.SkipDir
		}
		return addFileToTar(tw, path, absContext, info)
	})
}

// addFileToTar adds a single file or directory entry to the tar writer.
func addFileToTar(tw *tar.Writer, path, absContext string, info os.FileInfo) error {
	relPath, err := filepath.Rel(absContext, path)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = relPath
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	f, err := os.Open(path) // #nosec G304 G703 -- path from Walk
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(tw, f)
	return err
}

// addExternalDockerfile adds a Dockerfile to the tar if it lives outside the context.
func addExternalDockerfile(tw *tar.Writer, dockerfilePath, absContext string) error {
	absDockerfile, _ := filepath.Abs(dockerfilePath)
	if strings.HasPrefix(absDockerfile, absContext+string(filepath.Separator)) {
		return nil
	}
	data, err := os.ReadFile(absDockerfile) // #nosec G304 G703 -- validated path from project dir
	if err != nil {
		return err
	}
	header := &tar.Header{Name: "Dockerfile", Size: int64(len(data)), Mode: 0o644}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}
