// Package logrotate bounds the on-disk footprint of the append-mode log files
// human writes under ~/.human, without disturbing the running daemon that holds
// them open.
//
// The daemon and the chrome bridge inherit their log file as the child's raw
// stdout/stderr; the audit and destructive providers hold their file open for
// the process lifetime. None of these writers can be told to reopen a moved
// file, so rotation must never move the file out from under the open descriptor.
// Rotation therefore uses copytruncate: the live file's contents are copied to a
// numbered generation beside it and the live file is then truncated in place, so
// the descriptor keeps writing to the same inode. Every writer opens its file
// O_APPEND, so a write after truncation lands at the (now zero) end of file
// rather than leaving a sparse gap.
//
// Generations are named <name>.1, <name>.2, ... with <name>.1 the most recent
// rotation, following the widespread convention (daemon.log.1) rather than the
// one-off daemon.1.log the manual 2026-08-01 rotation produced. Generations are
// left uncompressed so older history stays directly greppable, which is the
// whole point of keeping it (SC-2611).
package logrotate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gethuman-sh/human/errors"
)

// Policy governs copytruncate rotation for a single log file.
type Policy struct {
	// MaxSizeBytes is the size at or above which the live file is rotated. A
	// non-positive value disables rotation for the file.
	MaxSizeBytes int64
	// MaxGenerations caps how many numbered older generations are retained; the
	// oldest beyond the cap is discarded on each rotation. A value of 0 means
	// unlimited: no generation is ever deleted, so an unattended daemon keeps the
	// full history of an accountability trail (audit.log, destructive.log) on
	// disk even as it rotates the file for readability.
	MaxGenerations int
}

// Rotate applies copytruncate rotation to the file at path according to policy.
// It reports whether a rotation happened. A missing file, or a file below the
// size threshold, is a no-op and never an error.
func Rotate(path string, policy Policy) (bool, error) {
	if policy.MaxSizeBytes <= 0 {
		return false, nil
	}

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, errors.WrapWithDetails(err, "stat log file", "path", path)
	}
	if info.Size() < policy.MaxSizeBytes {
		return false, nil
	}

	if err := shiftGenerations(path, policy.MaxGenerations); err != nil {
		return false, err
	}
	if err := copyFile(path, generationPath(path, 1), info.Mode().Perm()); err != nil {
		return false, err
	}
	// Truncating in place (rather than renaming the live file away) is what keeps
	// the open descriptor writing to the same inode across the rotation.
	if err := os.Truncate(path, 0); err != nil {
		return false, errors.WrapWithDetails(err, "truncate log file", "path", path)
	}
	return true, nil
}

// shiftGenerations renames each existing generation up by one (.1 -> .2, ...) so
// that .1 is free for the fresh copy. When maxGenerations > 0 the generation
// that would exceed the cap is discarded; when it is 0 nothing is ever deleted.
func shiftGenerations(path string, maxGenerations int) error {
	highest := highestGeneration(path)
	if maxGenerations > 0 {
		// Drop everything at or beyond the cap before shifting, so that after the
		// shift the newest generation occupies .1 and the total never exceeds the
		// cap. Anything above the cap (e.g. left by a shrunk policy) goes too.
		for n := highest; n >= maxGenerations; n-- {
			if err := removeIfExists(generationPath(path, n)); err != nil {
				return err
			}
		}
		if highest > maxGenerations-1 {
			highest = maxGenerations - 1
		}
	}
	for n := highest; n >= 1; n-- {
		src := generationPath(path, n)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		dst := generationPath(path, n+1)
		if err := os.Rename(src, dst); err != nil {
			return errors.WrapWithDetails(err, "shift log generation", "from", src, "to", dst)
		}
	}
	return nil
}

// highestGeneration returns the largest numbered generation currently on disk
// for path, or 0 if there are none. It scans the directory rather than probing
// sequentially so a gap in the numbering (from a manual delete) cannot hide
// older generations from the shift.
func highestGeneration(path string) int {
	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if err != nil || n < 1 {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest
}

func generationPath(path string, n int) string {
	return fmt.Sprintf("%s.%d", path, n)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errors.WrapWithDetails(err, "discard old log generation", "path", path)
	}
	return nil
}

// copyFile copies src to dst, creating dst with the given permission bits and
// flushing it to disk before returning so a crash right after rotation cannot
// leave a rotated generation half-written.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src) // #nosec G304 -- src is a daemon-owned log path, not user input
	if err != nil {
		return errors.WrapWithDetails(err, "open log file for rotation", "path", src)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm) // #nosec G304 -- dst is derived from a daemon-owned log path
	if err != nil {
		return errors.WrapWithDetails(err, "create rotated log generation", "path", dst)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return errors.WrapWithDetails(err, "copy log contents for rotation", "from", src, "to", dst)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return errors.WrapWithDetails(err, "flush rotated log generation", "path", dst)
	}
	if err := out.Close(); err != nil {
		return errors.WrapWithDetails(err, "close rotated log generation", "path", dst)
	}
	return nil
}
