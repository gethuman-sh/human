package devcontainer

import (
	"archive/tar"
	"context"
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/release"
)

// containerHumanPath is where the agent image expects `human`, and where the
// devcontainer feature already installs one.
const containerHumanPath = "/usr/local/bin/human"

// elfMachineFor maps a Docker/Go architecture name to the ELF machine a binary
// for it declares. An architecture with no entry cannot be checked beyond the
// format, which is deliberate: refusing an unmapped-but-correct binary would
// break a host this code has never been run on, for no evidence.
func elfMachineFor(arch string) elf.Machine {
	switch arch {
	case "amd64":
		return elf.EM_X86_64
	case "arm64":
		return elf.EM_AARCH64
	case "386":
		return elf.EM_386
	case "arm":
		return elf.EM_ARM
	case "riscv64":
		return elf.EM_RISCV
	default:
		return elf.EM_NONE
	}
}

// isLinuxELF reports whether path holds a binary the container can execute:
// an ELF, of the container's machine. Format alone is not enough — an ELF built
// for another CPU loads and dies exactly like a Mach-O one, which is the failure
// the launch is being taught to refuse rather than discover at runc init.
func isLinuxELF(path, arch string) bool {
	f, err := elf.Open(path) // #nosec G304 -- a candidate path this package composed
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	want := elfMachineFor(arch)
	return want == elf.EM_NONE || f.Machine == want
}

// binaryFetcher obtains the released linux human for a version at destPath. A
// func rather than an interface because there is exactly one operation and one
// production value (release.FetchBinary); a test substitutes it so no test ever
// reaches the network.
type binaryFetcher func(ctx context.Context, version, goos, goarch, destPath string, progress io.Writer) error

// cachedHumanPath is where a downloaded linux human for a version lives. The
// version is in the NAME on purpose: isLinuxELF can only see format and machine,
// so a version-less cache file would be indistinguishable from the right one
// after a daemon upgrade and would silently hand the container the previous
// release's CLI — the SC-4621 failure by a new route. Empty when there is no
// home directory or no released version, which is also how a launch says "do
// not cache and do not download".
func cachedHumanPath(homeDir, version, arch string) string {
	if homeDir == "" || version == "" {
		return ""
	}
	return filepath.Join(homeDir, ".human", "bin", "human-"+version+"-linux-"+arch)
}

// containerHumanCandidates names every place a launch looks for the linux human
// the container will run, in the order it looks. The checkout's own build wins:
// a developer who just cross-built must get that binary and not a stale cached
// download. The daemon's own executable comes last and only qualifies when it is
// itself an ELF of the container's architecture — that is a linux host handing
// the container its exact build, and it feeds a copy, never a Bind (SC-4631).
func containerHumanCandidates(arch, version, projectDir, homeDir, exePath string) []string {
	name := "human-linux-" + arch
	var out []string
	if projectDir != "" {
		out = append(out, filepath.Join(projectDir, "bin", name))
	}
	if exePath != "" {
		out = append(out, filepath.Join(filepath.Dir(exePath), name))
	}
	if cached := cachedHumanPath(homeDir, version, arch); cached != "" {
		out = append(out, cached)
	}
	if exePath != "" {
		out = append(out, exePath)
	}
	return out
}

// noBinaryError is the refusal when nothing local answers and nothing can be
// downloaded. The paths searched are embedded directly in the message, not only
// carried as structured details, so a human reading the failed-launch marker
// sees exactly which file to produce without also having to read the details
// map.
func noBinaryError(arch, version string, verErr error, candidates []string) error {
	searched := strings.Join(candidates, ", ")
	if verErr != nil {
		return errors.WithDetails(
			"no linux/%s human binary for the container and daemon version %q is not a published release to download one from: build one with `make build-linux` (searched %s)",
			"arch", arch, "version", version, "searched", searched)
	}
	return errors.WithDetails(
		"no linux/%s human binary for the container: build one with `make build-linux` (searched %s)",
		"arch", arch, "searched", searched, "version", version)
}

// resolveContainerHuman picks the binary to copy into the container: a local one
// if any host path holds it, otherwise the release build for the daemon's own
// version, downloaded once and cached. It is deliberately a refusal and not a
// fallback to the image's own human: that one lags the host by weeks and carries
// none of the marker, handoff, fsm, plan, commits, deploy or state commands an
// agent needs to record its work, so a container built on it runs and then fails
// silently.
func resolveContainerHuman(ctx context.Context, arch, version, projectDir, homeDir, exePath string,
	fetch binaryFetcher, out io.Writer) (string, error) {
	// An unreleased stamp yields "", which drops the cache candidate and is
	// carried into the message — never into a URL.
	num, verErr := release.Version(version)

	candidates := containerHumanCandidates(arch, num, projectDir, homeDir, exePath)
	for _, c := range candidates {
		if isLinuxELF(c, arch) {
			return c, nil
		}
	}

	dest := cachedHumanPath(homeDir, num, arch)
	if verErr != nil || fetch == nil || dest == "" {
		return "", noBinaryError(arch, version, verErr, candidates)
	}

	if err := fetch(ctx, num, "linux", arch, dest, out); err != nil {
		// The cause is spelled into the message as well as wrapped: a launch
		// failure is read off a marker comment, where only the message shows.
		return "", errors.WrapWithDetails(err,
			"obtaining the linux/%s human %s for the container: %s (searched %s)",
			"arch", arch, "version", num, "cause", err.Error(), "searched", strings.Join(candidates, ", "))
	}
	if !isLinuxELF(dest, arch) {
		// A download that verified its checksum but is not the expected machine
		// means the release names an artifact this host cannot use; keeping it
		// would make every later launch skip the fetch and reuse the bad file.
		// #nosec G703 -- dest is the cache path this package composed from the home dir, version and arch
		_ = os.Remove(dest)
		return "", errors.WithDetails("downloaded human is not a linux/%s binary",
			"arch", arch, "version", num, "path", dest)
	}
	pruneStaleCache(filepath.Dir(dest), filepath.Base(dest), arch)
	return dest, nil
}

// staleTempAge is how long an abandoned download temp file must have sat before
// pruning takes it. It is longer than the fetch client's own timeout, so a file
// this old cannot be an in-flight download of a concurrent launch — removing one
// of those would make that launch's rename fail.
const staleTempAge = time.Hour

// pruneStaleCache removes previously downloaded linux humans for other versions,
// and temp files abandoned by an interrupted download, so the cache holds one
// ~50 MB binary per architecture rather than one per release ever launched.
// Best effort: a failure here must never fail a launch.
func pruneStaleCache(dir, keep, arch string) {
	pruneMatching(filepath.Join(dir, "human-*-linux-"+arch), keep, 0)
	pruneMatching(filepath.Join(dir, ".human-dl-*"), keep, staleTempAge)
	pruneMatching(filepath.Join(dir, ".human-bin-*"), keep, staleTempAge)
}

// pruneMatching removes every match of pattern except keep, skipping files younger than
// minAge when one is given.
func pruneMatching(pattern, keep string, minAge time.Duration) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, m := range matches {
		if filepath.Base(m) == keep {
			continue
		}
		if minAge > 0 {
			// #nosec G703 -- m is a Glob match inside the cache directory, against a pattern this package owns
			fi, err := os.Stat(m)
			if err != nil || time.Since(fi.ModTime()) < minAge {
				continue
			}
		}
		// #nosec G703 -- m is a Glob match inside the cache directory, against a pattern this package owns
		_ = os.Remove(m)
	}
}

// tarSingleFile streams path as a one-entry tar named name, mode 0755. It
// streams rather than buffering because the binary is ~67 MB and a launch
// should not hold two copies of it in the daemon's heap.
func tarSingleFile(path, name string) (io.ReadCloser, error) {
	f, err := os.Open(path) // #nosec G304 -- a candidate path this package resolved
	if err != nil {
		return nil, errors.WrapWithDetails(err, "opening container human binary", "path", path)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, errors.WrapWithDetails(err, "sizing container human binary", "path", path)
	}

	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = f.Close() }()
		tw := tar.NewWriter(pw)
		hdr := &tar.Header{
			Name: name, Mode: 0o755, Size: fi.Size(),
			ModTime: fi.ModTime(), Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(tw, f); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.CloseWithError(tw.Close())
	}()
	return pr, nil
}

// injectContainerHuman puts the resolved binary at containerHumanPath before the
// container starts, so the postStartCommand — which runs `human` — already has
// the right one. Transport is a copy and not a bind: Docker Desktop on macOS
// shares only a fixed list of directories and materializes anything else as an
// empty directory, which then fails runc init against the image's own file. The
// daemon's process reaches its own install directory regardless (SC-4631).
func (m *Manager) injectContainerHuman(ctx context.Context, containerID, binPath string) error {
	content, err := tarSingleFile(binPath, filepath.Base(containerHumanPath))
	if err != nil {
		return err
	}
	defer func() { _ = content.Close() }()

	if err := m.Docker.CopyToContainer(ctx, containerID, filepath.Dir(containerHumanPath), content); err != nil {
		return errors.WrapWithDetails(err, "copying human binary into container",
			"path", binPath, "target", containerHumanPath)
	}
	return nil
}

// containerArch is the architecture the container will actually run, read from
// the image rather than assumed from the host: a host may run an image of the
// other architecture, and the binary must match the image.
func (m *Manager) containerArch(ctx context.Context, imageName string) string {
	if resp, err := m.Docker.ImageInspect(ctx, imageName); err == nil && resp.Architecture != "" {
		return resp.Architecture
	}
	return runtime.GOARCH
}

// hostExecutablePath is the daemon's own path, as a copy source only.
func (m *Manager) hostExecutablePath() string {
	lookup := m.hostExecutable
	if lookup == nil {
		lookup = os.Executable
	}
	path, err := lookup()
	if err != nil {
		return ""
	}
	return path
}
