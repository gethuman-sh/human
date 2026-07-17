package agent

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gethuman-sh/human/errors"
	"github.com/gethuman-sh/human/internal/devcontainer"
)

// containerTranscriptPath is the in-container Claude session transcript root.
// The remote user's home is at /home/<user>; an empty or root user falls back
// to /root.
func containerTranscriptPath(remoteUser string) string {
	if remoteUser == "" || remoteUser == "root" {
		return "/root/.claude/projects"
	}
	return "/home/" + remoteUser + "/.claude/projects"
}

// CopyTranscript streams ~/.claude/projects from the container as a tar archive
// and extracts it into destDir. It is best-effort: a missing path or copy error
// is returned wrapped, but callers treat copy-out as non-fatal — the container
// removal must still proceed. Extraction is hardened against path traversal:
// entries that would escape destDir are skipped.
func CopyTranscript(ctx context.Context, docker devcontainer.DockerClient, containerID, remoteUser, destDir string) error {
	srcPath := containerTranscriptPath(remoteUser)
	rc, err := docker.CopyFromContainer(ctx, containerID, srcPath)
	if err != nil {
		return errors.WrapWithDetails(err, "copying transcript from container", "container", containerID, "src", srcPath)
	}
	defer func() { _ = rc.Close() }()

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating transcript directory", "dir", destDir)
	}

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.WrapWithDetails(err, "reading transcript archive")
		}
		if err := extractTarEntry(tr, hdr, destDir); err != nil {
			return err
		}
	}
	return nil
}

// extractTarEntry writes one tar entry under destDir, rejecting any name that
// would escape destDir (path traversal).
func extractTarEntry(tr *tar.Reader, hdr *tar.Header, destDir string) error {
	clean := filepath.Clean(hdr.Name)
	target := filepath.Join(destDir, clean)
	// The joined target must remain within destDir; anything else is a traversal
	// attempt and is skipped rather than trusted.
	if target != destDir && !strings.HasPrefix(target, destDir+string(os.PathSeparator)) {
		return nil
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, 0o700); err != nil {
			return errors.WrapWithDetails(err, "creating transcript subdir", "dir", target)
		}
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return errors.WrapWithDetails(err, "creating transcript parent", "dir", filepath.Dir(target))
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- target validated to stay within destDir above
		if err != nil {
			return errors.WrapWithDetails(err, "creating transcript file", "path", target)
		}
		// Bound the copy so a hostile archive cannot fill the disk via one entry.
		if _, err := io.Copy(f, io.LimitReader(tr, maxTranscriptFileBytes)); err != nil {
			_ = f.Close()
			return errors.WrapWithDetails(err, "writing transcript file", "path", target)
		}
		if err := f.Close(); err != nil {
			return errors.WrapWithDetails(err, "closing transcript file", "path", target)
		}
	}
	return nil
}

// maxTranscriptFileBytes caps a single extracted transcript file so a malformed
// or hostile archive cannot exhaust host disk through one giant entry. Claude
// session transcripts are JSONL and stay well under this.
const maxTranscriptFileBytes = 64 << 20 // 64 MiB
