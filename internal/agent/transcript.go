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

// CopyTranscript preserves the Claude session transcript for one run out of the
// container into destDir. One container hosts many runs, each in its own private
// worktree, and Claude stores every run's sessions in a per-cwd subdir of
// ~/.claude/projects. When the run's worktree is known, the copy-out is scoped to
// just that subdir, so a preserved run carries its own sessions and no others
// (SC-2463). If the worktree is empty or its subdir cannot be copied — an
// unrecognised cwd encoding, or a run whose sessions never materialised — the
// whole projects root is copied instead: losing the evidence is worse than
// keeping too much of it. Best-effort by contract; callers treat any error as a
// skip and proceed with container removal.
func CopyTranscript(ctx context.Context, docker devcontainer.DockerClient, containerID, remoteUser, worktree, destDir string) error {
	root := containerTranscriptPath(remoteUser)
	if worktree != "" {
		scoped := root + "/" + claudeProjectDirName(worktree)
		if err := copyTranscriptTree(ctx, docker, containerID, scoped, destDir); err == nil {
			return nil
		}
		// Scoped copy failed (subdir absent, or the cwd encoding did not match this
		// Claude build): fall back to the whole root — preserve more, not less.
	}
	return copyTranscriptTree(ctx, docker, containerID, root, destDir)
}

// claudeProjectDirName reproduces Claude Code's cwd→project-dir encoding: every
// character that is not an ASCII letter or digit becomes '-'. Claude names each
// ~/.claude/projects/<encoded-cwd> subdir this way, so a run's private worktree
// path (its cwd) maps to exactly the subdir holding that run's sessions. Verified
// against a live index: "/workspaces/cli" -> "-workspaces-cli".
func claudeProjectDirName(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// copyTranscriptTree streams srcPath from the container as a tar archive and
// extracts it into destDir. It is best-effort: a missing path or copy error is
// returned wrapped, but callers treat copy-out as non-fatal — the container
// removal must still proceed. Extraction is hardened against path traversal:
// non-local entry names are skipped and every filesystem operation goes through
// an os.Root bound to destDir, so containment is kernel-enforced.
func copyTranscriptTree(ctx context.Context, docker devcontainer.DockerClient, containerID, srcPath, destDir string) error {
	rc, err := docker.CopyFromContainer(ctx, containerID, srcPath)
	if err != nil {
		return errors.WrapWithDetails(err, "copying transcript from container", "container", containerID, "src", srcPath)
	}
	defer func() { _ = rc.Close() }()
	// A stalled docker tar stream must not park the caller forever: the watchdog
	// closes rc when ctx is cancelled so the blocking tr.Next()/io.Copy below
	// return an error, which callers treat as a best-effort skip (SC-427). LIFO
	// defer order tears the watchdog down before the final rc.Close above;
	// double-close is safe.
	defer closeOnContextDone(ctx, rc)()

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating transcript directory", "dir", destDir)
	}
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return errors.WrapWithDetails(err, "opening transcript directory root", "dir", destDir)
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.WrapWithDetails(err, "reading transcript archive")
		}
		if err := extractTarEntry(tr, hdr, root); err != nil {
			return err
		}
	}
	return nil
}

// closeOnContextDone starts a watchdog that closes c when ctx is cancelled,
// unblocking a read that is parked on a stalled stream. It returns a stop func
// the caller must invoke (typically via defer) to tear the watchdog down once
// the read has finished on its own; stop also releases the watchdog goroutine.
func closeOnContextDone(ctx context.Context, c io.Closer) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

// extractTarEntry writes one tar entry through the os.Root bound to the
// destination, so escaping it — via .. segments or any symlink encountered on
// the way — fails at the kernel, not on a string check. Non-local names
// (including absolute ones; docker tars are always relative) are skipped
// rather than trusted.
func extractTarEntry(tr *tar.Reader, hdr *tar.Header, root *os.Root) error {
	clean := filepath.Clean(hdr.Name)
	if !filepath.IsLocal(clean) {
		return nil
	}

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(clean, 0o700); err != nil {
			return errors.WrapWithDetails(err, "creating transcript subdir", "dir", clean)
		}
	case tar.TypeReg:
		if parent := filepath.Dir(clean); parent != "." {
			if err := root.MkdirAll(parent, 0o700); err != nil {
				return errors.WrapWithDetails(err, "creating transcript parent", "dir", parent)
			}
		}
		f, err := root.OpenFile(clean, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return errors.WrapWithDetails(err, "creating transcript file", "path", clean)
		}
		// Bound the copy so a hostile archive cannot fill the disk via one entry.
		if _, err := io.Copy(f, io.LimitReader(tr, maxTranscriptFileBytes)); err != nil {
			_ = f.Close()
			return errors.WrapWithDetails(err, "writing transcript file", "path", clean)
		}
		if err := f.Close(); err != nil {
			return errors.WrapWithDetails(err, "closing transcript file", "path", clean)
		}
	}
	return nil
}

// maxTranscriptFileBytes caps a single extracted transcript file so a malformed
// or hostile archive cannot exhaust host disk through one giant entry. Claude
// session transcripts are JSONL and stay well under this.
const maxTranscriptFileBytes = 64 << 20 // 64 MiB
