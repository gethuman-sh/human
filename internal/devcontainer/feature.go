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

	"github.com/gethuman-sh/human/errors"
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

	// Pull every feature's metadata up front: the install order depends on each
	// feature's installsAfter edges, which are only known after Pull. Caching the
	// tarball and meta here also avoids a second Pull during installation.
	pulled := make(map[string]*pulledFeature, len(features))
	metas := make(map[string]*FeatureMeta, len(features))
	for ref := range features {
		tarData, meta, err := puller.Pull(ctx, ref)
		if err != nil {
			return errors.WrapWithDetails(err, "pulling feature", "ref", ref)
		}
		pulled[ref] = &pulledFeature{tarData: tarData, meta: meta}
		metas[ref] = meta
	}

	// Order features so each is installed only after the features it lists in
	// installsAfter (when those features are present in the set).
	refs, err := orderFeatures(metas)
	if err != nil {
		return err
	}

	for _, ref := range refs {
		opts, _ := features[ref].(map[string]interface{})
		_, _ = fmt.Fprintf(out, "  Installing feature: %s\n", ref) // #nosec G705 -- CLI output

		pf := pulled[ref]
		if pf == nil {
			return errors.WithDetails("ordered feature missing pulled data", "ref", ref)
		}
		env := featureEnv(opts, pf.meta, remoteUser)

		if err := copyAndRunFeature(ctx, docker, containerID, pf.tarData, env, logger); err != nil {
			return errors.WrapWithDetails(err, "installing feature", "ref", ref)
		}

		logger.Info().Str("feature", ref).Msg("feature installed")
	}

	return nil
}

// pulledFeature holds a feature's downloaded tarball and parsed metadata so the
// install loop can run without re-pulling after ordering.
type pulledFeature struct {
	tarData []byte
	meta    *FeatureMeta
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

	// Clean up. Non-fatal, but warn on failure: a left-behind staging dir gets
	// baked into the committed image (along with the feature tarball and any
	// option values it carries) and reused for every subsequent container.
	cleanCmd := []string{"/bin/sh", "-c", "rm -rf /tmp/devcontainer-feature"}
	if err := execInContainer(ctx, docker, containerID, "root", cleanCmd, nil, logger); err != nil {
		logger.Warn().Err(err).Msg("failed to remove staged feature directory; it will be baked into the image")
	}

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

// normalizeRef reduces a feature reference to its registry/repository part,
// dropping any tag or digest. installsAfter entries are conventionally untagged
// (ghcr.io/devcontainers/features/node) while config refs are tagged
// (ghcr.io/devcontainers/features/node:1); edge matching must compare on the
// repository part so the two resolve to the same node.
func normalizeRef(ref string) string {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		// Fall back to a tag/digest strip so an unparseable ref still matches
		// deterministically rather than silently never matching.
		ref = strings.TrimSuffix(ref, "/")
		if at := strings.LastIndex(ref, "@"); at != -1 {
			ref = ref[:at]
		}
		if slash := strings.LastIndex(ref, "/"); slash != -1 {
			if colon := strings.LastIndex(ref[slash:], ":"); colon != -1 {
				ref = ref[:slash+colon]
			}
		}
		return ref
	}
	return parsed.Context().Name()
}

// orderFeatures returns feature references in installation order, respecting
// each feature's installsAfter edges (restricted to features present in the
// set) and using alphabetical order as a deterministic tie-break. Edges to
// features that are absent from the set are ignored. A cycle in the edges is
// reported as an error rather than looping.
func orderFeatures(metas map[string]*FeatureMeta) ([]string, error) {
	refs := make([]string, 0, len(metas))
	for ref := range metas {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	dependents, indegree := buildFeatureGraph(refs, metas)

	order := kahnSort(refs, dependents, indegree)

	if len(order) != len(refs) {
		remaining := make([]string, 0, len(refs)-len(order))
		for _, ref := range refs {
			if indegree[ref] > 0 {
				remaining = append(remaining, ref)
			}
		}
		sort.Strings(remaining)
		return nil, errors.WithDetails("feature installsAfter cycle", "features", remaining)
	}

	return order, nil
}

// buildFeatureGraph constructs the installsAfter dependency graph over the
// present features. An edge dep -> ref means dep must install before ref.
// Edges to absent features and self-edges are ignored; duplicate edges are
// collapsed so the returned indegree stays accurate.
func buildFeatureGraph(refs []string, metas map[string]*FeatureMeta) (dependents map[string][]string, indegree map[string]int) {
	// Map each present feature's normalized repository to its config ref so
	// untagged installsAfter entries can resolve to tagged config refs.
	byRepo := make(map[string]string, len(refs))
	for _, ref := range refs {
		byRepo[normalizeRef(ref)] = ref
	}

	dependents = make(map[string][]string, len(refs))
	indegree = make(map[string]int, len(refs))
	for _, ref := range refs {
		indegree[ref] = 0
	}
	for _, ref := range refs {
		meta := metas[ref]
		if meta == nil {
			continue
		}
		seen := make(map[string]struct{})
		for _, after := range meta.InstallsAfter {
			dep, present := byRepo[normalizeRef(after)]
			if !present || dep == ref {
				continue
			}
			if _, dup := seen[dep]; dup {
				continue
			}
			seen[dep] = struct{}{}
			dependents[dep] = append(dependents[dep], ref)
			indegree[ref]++
		}
	}
	return dependents, indegree
}

// kahnSort topologically sorts refs via Kahn's algorithm, picking the
// alphabetically smallest install-ready node at each step for determinism.
// A returned order shorter than refs signals a cycle (nodes with residual
// indegree were never released).
func kahnSort(refs []string, dependents map[string][]string, indegree map[string]int) []string {
	ready := make([]string, 0, len(refs))
	for _, ref := range refs {
		if indegree[ref] == 0 {
			ready = append(ready, ref)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(refs))
	for len(ready) > 0 {
		ref := ready[0]
		ready = ready[1:]
		order = append(order, ref)

		newlyReady := make([]string, 0)
		for _, dep := range dependents[ref] {
			indegree[dep]--
			if indegree[dep] == 0 {
				newlyReady = append(newlyReady, dep)
			}
		}
		if len(newlyReady) > 0 {
			ready = append(ready, newlyReady...)
			sort.Strings(ready)
		}
	}
	return order
}
