package index

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/odvcencio/gotreesitter/grammars"
)

// OnDiskFile is a source file codenav would index, with cheap stat metadata
// (no content hash — hashing is deferred to change detection).
type OnDiskFile struct {
	Rel, Abs    string
	Size, MTime int64
}

// ScanSources walks scan.Root and returns every source file codenav indexes
// (Go plus curated tree-sitter languages), applying skipDirs. The include rule
// is identical to the removed SourceSignature so nothing new is indexed.
func ScanSources(scan RepoScan) []OnDiskFile {
	var out []OnDiskFile
	_ = filepath.WalkDir(scan.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSourceName(d.Name()) {
			return nil
		}
		rel, ok := relWithin(scan.Root, path)
		if !ok {
			return nil
		}
		var size, mtime int64
		if info, ierr := d.Info(); ierr == nil {
			size, mtime = info.Size(), info.ModTime().Unix()
		}
		out = append(out, OnDiskFile{Rel: rel, Abs: path, Size: size, MTime: mtime})
		return nil
	})
	return out
}

// isSourceName reports whether a file's basename is one codenav indexes: Go
// files plus curated tree-sitter languages. Shared by the walk-based scanners.
func isSourceName(name string) bool {
	if strings.HasSuffix(name, ".go") {
		return true
	}
	if e := grammars.DetectLanguage(name); e != nil && tsLangs[e.Name] {
		return true
	}
	return false
}

// HashFile returns the hex SHA-256 of a source file, the content fingerprint
// change detection compares against the stored hash. Empty on read error.
func HashFile(path string) string { return hashFile(path) }
