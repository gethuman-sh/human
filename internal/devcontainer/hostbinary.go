package devcontainer

import (
	"archive/tar"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gethuman-sh/human/errors"
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

// containerHumanCandidates names every place a launch looks for the linux human
// the container will run, in the order it looks. The checkout's own build wins:
// a developer who just cross-built must get that binary and not a stale cached
// download. The daemon's own executable comes last and only qualifies when it is
// itself an ELF of the container's architecture — that is a linux host handing
// the container its exact build, and it feeds a copy, never a Bind (SC-4631).
func containerHumanCandidates(arch, projectDir, homeDir, exePath string) []string {
	name := "human-linux-" + arch
	var out []string
	if projectDir != "" {
		out = append(out, filepath.Join(projectDir, "bin", name))
	}
	if exePath != "" {
		out = append(out, filepath.Join(filepath.Dir(exePath), name))
	}
	if homeDir != "" {
		out = append(out, filepath.Join(homeDir, ".human", "bin", name))
	}
	if exePath != "" {
		out = append(out, exePath)
	}
	return out
}

// resolveContainerHuman picks the binary to copy into the container, or says
// which paths it looked at. It is deliberately a refusal and not a fallback to
// the image's own human: that one lags the host by weeks and carries none of the
// marker, handoff, fsm, plan, commits, deploy or state commands an agent needs to
// record its work, so a container built on it runs and then fails silently.
func resolveContainerHuman(arch, projectDir, homeDir, exePath string) (string, error) {
	candidates := containerHumanCandidates(arch, projectDir, homeDir, exePath)
	for _, c := range candidates {
		if isLinuxELF(c, arch) {
			return c, nil
		}
	}
	// The paths searched are embedded directly in the message, not only carried
	// as structured details, so a human reading the failed-launch marker sees
	// exactly which file to produce without also having to read the details map.
	msg := fmt.Sprintf(
		"no linux/%s human binary for the container: build one with `make build-linux` (searched %s)",
		arch, strings.Join(candidates, ", "),
	)
	return "", errors.WithDetails(msg, "arch", arch, "searched", candidates)
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
