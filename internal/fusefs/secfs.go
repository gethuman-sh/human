//go:build linux

package fusefs

import (
	"context"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// SecNode is a FUSE inode that mirrors a real file or directory.
// For non-sensitive files, it delegates to LoopbackNode (passthrough).
// For sensitive files, it serves empty or redacted content and blocks writes.
type SecNode struct {
	*fs.LoopbackNode
	safeMode bool
}

var _ = (fs.NodeWrapChilder)((*SecNode)(nil))
var _ = (fs.NodeOpener)((*SecNode)(nil))
var _ = (fs.NodeGetattrer)((*SecNode)(nil))

// WrapChild ensures every child inode created by Lookup/Create is a SecNode.
func (n *SecNode) WrapChild(_ context.Context, ops fs.InodeEmbedder) fs.InodeEmbedder {
	lb, ok := ops.(*fs.LoopbackNode)
	if !ok {
		return ops
	}
	return &SecNode{LoopbackNode: lb, safeMode: n.safeMode}
}

func (n *SecNode) fileKind() FileKind {
	// Check the FUSE path name first.
	if kind := IsSensitiveFile(filepath.Base(n.Path(n.root()))); kind != FileKindNone {
		return kind
	}
	// Also resolve symlinks to check the real target's name,
	// preventing bypass via symlinks with non-sensitive names.
	real := n.realPath()
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil || resolved == real {
		return FileKindNone
	}
	return IsSensitiveFile(filepath.Base(resolved))
}

func (n *SecNode) root() *fs.Inode {
	if n.RootData.RootNode != nil {
		return n.RootData.RootNode.EmbeddedInode()
	}
	return n.Root()
}

// realPath returns the full path to the backing file on disk.
func (n *SecNode) realPath() string {
	return filepath.Join(n.RootData.Path, n.Path(n.root()))
}

// loadRedacted reads and redacts the backing file once so Open and
// Getattr can share a single snapshot. Returning the same bytes to
// both paths avoids the content/size mismatch that would otherwise
// happen when the underlying file is rewritten between the kernel's
// Getattr-before-Open and the subsequent Open call.
func (n *SecNode) loadRedacted(kind FileKind) ([]byte, error) {
	content, err := os.ReadFile(n.realPath())
	if err != nil {
		return nil, err
	}
	switch kind {
	case FileKindEnv:
		return RedactEnv(content), nil
	case FileKindJSON, FileKindYAML:
		return nil, nil
	default:
		return nil, nil
	}
}

// Open intercepts sensitive files and returns a protected handle.
// All other files delegate to LoopbackNode.Open for passthrough I/O.
func (n *SecNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	kind := n.fileKind()
	if kind == FileKindNone {
		return n.LoopbackNode.Open(ctx, flags)
	}

	// Safe mode or opaque files: return empty content.
	if n.safeMode || kind == FileKindOpaque {
		return &emptyFileHandle{}, fuse.FOPEN_DIRECT_IO, fs.OK
	}

	redacted, err := n.loadRedacted(kind)
	if err != nil {
		return nil, 0, fs.ToErrno(err)
	}
	return &redactedFileHandle{data: redacted}, fuse.FOPEN_DIRECT_IO, fs.OK
}

// Getattr for sensitive files reports the redacted (or zero) size.
func (n *SecNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	errno := n.LoopbackNode.Getattr(ctx, f, out)
	if errno != fs.OK {
		return errno
	}

	kind := n.fileKind()
	if kind == FileKindNone {
		return fs.OK
	}

	// Safe mode or opaque files: report size 0.
	if n.safeMode || kind == FileKindOpaque {
		out.Size = 0
		return fs.OK
	}

	redacted, err := n.loadRedacted(kind)
	if err != nil {
		out.Size = 0
		return fs.OK
	}
	out.Size = uint64(len(redacted))
	return fs.OK
}

// emptyFileHandle serves empty content and rejects writes.
type emptyFileHandle struct{}

var _ = (fs.FileReader)((*emptyFileHandle)(nil))
var _ = (fs.FileWriter)((*emptyFileHandle)(nil))
var _ = (fs.FileGetattrer)((*emptyFileHandle)(nil))

func (h *emptyFileHandle) Read(_ context.Context, _ []byte, _ int64) (fuse.ReadResult, syscall.Errno) {
	return fuse.ReadResultData(nil), fs.OK
}

func (h *emptyFileHandle) Write(_ context.Context, _ []byte, _ int64) (uint32, syscall.Errno) {
	return 0, syscall.EROFS
}

func (h *emptyFileHandle) Getattr(_ context.Context, out *fuse.AttrOut) syscall.Errno {
	out.Size = 0
	return fs.OK
}

// redactedFileHandle serves redacted content from memory and rejects writes.
type redactedFileHandle struct {
	data []byte
}

var _ = (fs.FileReader)((*redactedFileHandle)(nil))
var _ = (fs.FileWriter)((*redactedFileHandle)(nil))
var _ = (fs.FileGetattrer)((*redactedFileHandle)(nil))

func (h *redactedFileHandle) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if off >= int64(len(h.data)) {
		return fuse.ReadResultData(nil), fs.OK
	}
	end := min(off+int64(len(dest)), int64(len(h.data)))
	return fuse.ReadResultData(h.data[off:end]), fs.OK
}

func (h *redactedFileHandle) Write(_ context.Context, _ []byte, _ int64) (uint32, syscall.Errno) {
	return 0, syscall.EROFS
}

func (h *redactedFileHandle) Getattr(_ context.Context, out *fuse.AttrOut) syscall.Errno {
	out.Size = uint64(len(h.data))
	return fs.OK
}

// NewSecRoot creates a new SecNode-based loopback root for the given directory.
func NewSecRoot(rootPath string, safeMode bool) (fs.InodeEmbedder, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(rootPath, &st); err != nil {
		return nil, err
	}

	root := &fs.LoopbackRoot{
		Path: rootPath,
		Dev:  uint64(st.Dev),
	}

	lb := &fs.LoopbackNode{RootData: root}
	sec := &SecNode{LoopbackNode: lb, safeMode: safeMode}
	root.RootNode = sec
	return sec, nil
}
