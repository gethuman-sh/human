package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/rs/zerolog"

	"github.com/StephanSchmidt/human/errors"
)

// FeatureMeta is the parsed devcontainer-feature.json from a feature tarball.
type FeatureMeta struct {
	ID            string                   `json:"id"`
	Version       string                   `json:"version"`
	Name          string                   `json:"name"`
	Options       map[string]FeatureOption `json:"options"`
	InstallsAfter []string                 `json:"installsAfter"`
	ContainerEnv  map[string]string        `json:"containerEnv"`
}

// FeatureOption describes a single feature option.
type FeatureOption struct {
	Type    string      `json:"type"`
	Default interface{} `json:"default"`
}

// FeaturePuller abstracts OCI feature download for testability.
type FeaturePuller interface {
	Pull(ctx context.Context, ref string) (installSh []byte, meta *FeatureMeta, err error)
}

// OCIFeaturePuller downloads devcontainer features from OCI registries.
type OCIFeaturePuller struct{}

// Pull downloads a devcontainer feature OCI artifact and returns the raw
// tarball bytes and parsed metadata.
func (p *OCIFeaturePuller) Pull(_ context.Context, featureRef string) ([]byte, *FeatureMeta, error) {
	ref, err := name.ParseReference(featureRef)
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "parsing feature reference", "ref", featureRef)
	}

	desc, err := remote.Get(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "fetching feature manifest", "ref", featureRef)
	}

	img, err := desc.Image()
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "converting feature descriptor to image", "ref", featureRef)
	}

	layers, err := img.Layers()
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "getting feature layers", "ref", featureRef)
	}
	if len(layers) == 0 {
		return nil, nil, errors.WithDetails("feature has no layers", "ref", featureRef)
	}

	// The layer is a plain tar (not gzipped) despite the method name.
	rc, err := layers[0].Compressed()
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "reading feature layer", "ref", featureRef)
	}
	defer func() { _ = rc.Close() }()

	tarData, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, errors.WrapWithDetails(err, "reading feature tarball", "ref", featureRef)
	}

	meta, err := extractFeatureMeta(tarData, featureRef)
	if err != nil {
		return nil, nil, err
	}

	return tarData, meta, nil
}

// extractFeatureMeta reads devcontainer-feature.json from a feature tarball.
func extractFeatureMeta(tarData []byte, ref string) (*FeatureMeta, error) {
	tr := tar.NewReader(bytes.NewReader(tarData))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.WrapWithDetails(err, "reading feature tar", "ref", ref)
		}
		entryName := strings.TrimPrefix(header.Name, "./")
		if entryName == "devcontainer-feature.json" {
			data, readErr := io.ReadAll(tr)
			if readErr != nil {
				return nil, errors.WrapWithDetails(readErr, "reading devcontainer-feature.json", "ref", ref)
			}
			var m FeatureMeta
			if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
				return nil, errors.WrapWithDetails(jsonErr, "parsing devcontainer-feature.json", "ref", ref)
			}
			return &m, nil
		}
	}
	return &FeatureMeta{}, nil
}

// InstallFeatures downloads and installs devcontainer features into a running
// container using exec. Each feature's install.sh is copied in via base64
// encoding, then executed with the appropriate option environment variables.
func InstallFeatures(ctx context.Context, docker DockerClient, puller FeaturePuller,
	containerID string, features map[string]interface{}, remoteUser string,
	logger zerolog.Logger, out io.Writer) error {

	if len(features) == 0 {
		return nil
	}

	// Sort feature refs for deterministic installation order.
	refs := sortedFeatureRefs(features)

	for _, ref := range refs {
		opts, _ := features[ref].(map[string]interface{})
		_, _ = fmt.Fprintf(out, "  Installing feature: %s\n", ref) // #nosec G705 -- CLI output

		tarData, meta, err := puller.Pull(ctx, ref)
		if err != nil {
			return errors.WrapWithDetails(err, "pulling feature", "ref", ref)
		}

		env := featureEnv(opts, meta, remoteUser)

		if err := copyAndRunFeature(ctx, docker, containerID, tarData, env, logger); err != nil {
			return errors.WrapWithDetails(err, "installing feature", "ref", ref)
		}

		logger.Info().Str("feature", ref).Msg("feature installed")
	}

	return nil
}

// copyAndRunFeature copies the entire feature tarball into the container using
// Docker's CopyToContainer API and runs install.sh.
func copyAndRunFeature(ctx context.Context, docker DockerClient, containerID string, tarData []byte, env []string, logger zerolog.Logger) error {
	// Create a staging directory inside the container.
	mkdirCmd := []string{"/bin/sh", "-c", "rm -rf /tmp/devcontainer-feature && mkdir -p /tmp/devcontainer-feature"}
	if err := execInContainer(ctx, docker, containerID, "root", mkdirCmd, nil, logger); err != nil {
		return errors.WrapWithDetails(err, "creating feature staging directory")
	}

	// Copy the entire feature tarball into the container.
	if err := docker.CopyToContainer(ctx, containerID, "/tmp/devcontainer-feature", bytes.NewReader(tarData)); err != nil {
		return errors.WrapWithDetails(err, "copying feature tarball to container")
	}

	// Make install.sh executable and run it from the feature directory.
	runCmd := []string{"/bin/sh", "-c",
		"cd /tmp/devcontainer-feature && chmod +x install.sh && ./install.sh"}
	if err := execInContainer(ctx, docker, containerID, "root", runCmd, env, logger); err != nil {
		return errors.WrapWithDetails(err, "running install.sh")
	}

	// Clean up.
	cleanCmd := []string{"/bin/sh", "-c", "rm -rf /tmp/devcontainer-feature"}
	_ = execInContainer(ctx, docker, containerID, "root", cleanCmd, nil, logger)

	return nil
}

var nonWordChars = regexp.MustCompile(`[^\w]`)

// featureEnv converts feature options to environment variables as specified
// by the devcontainer features spec. Option keys are uppercased with
// non-word characters replaced by underscores.
func featureEnv(opts map[string]interface{}, meta *FeatureMeta, remoteUser string) []string {
	env := []string{
		"_REMOTE_USER=" + remoteUser,
		"_CONTAINER_USER=" + remoteUser,
	}

	if remoteUser == "root" {
		env = append(env, "_REMOTE_USER_HOME=/root", "_CONTAINER_USER_HOME=/root")
	} else {
		env = append(env, "_REMOTE_USER_HOME=/home/"+remoteUser, "_CONTAINER_USER_HOME=/home/"+remoteUser)
	}

	// Apply defaults from feature metadata, then override with user options.
	merged := make(map[string]interface{})
	if meta != nil {
		for k, opt := range meta.Options {
			if opt.Default != nil {
				merged[k] = opt.Default
			}
		}
	}
	for k, v := range opts {
		merged[k] = v
	}

	for k, v := range merged {
		envKey := strings.ToUpper(nonWordChars.ReplaceAllString(k, "_"))
		env = append(env, envKey+"="+fmt.Sprint(v))
	}

	return env
}

// sortedFeatureRefs returns feature references in deterministic order
// (lexicographic by ref string).
func sortedFeatureRefs(features map[string]interface{}) []string {
	refs := make([]string, 0, len(features))
	for ref := range features {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}
