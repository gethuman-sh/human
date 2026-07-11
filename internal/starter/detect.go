package starter

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	humanerrors "github.com/gethuman-sh/human/errors"
)

// skipDirs are dependency and output directories whose contents say nothing
// about whether the user has written a project of their own (a stray
// node_modules must not suppress the wizard). Dot-directories are skipped by a
// blanket rule in IsEmptyProject, which also covers .git, .claude, .vscode etc.
var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"__pycache__":  true,
	"venv":         true,
	".venv":        true,
}

// sourceExtensions marks a file as project source by extension. The set is
// deliberately broad across languages: one hit anywhere means "there is a
// project here" and the wizard must stay away.
var sourceExtensions = map[string]bool{
	".go": true, ".rs": true, ".js": true, ".jsx": true, ".ts": true,
	".tsx": true, ".py": true, ".java": true, ".kt": true, ".rb": true,
	".php": true, ".c": true, ".h": true, ".cpp": true, ".cc": true,
	".hpp": true, ".cs": true, ".swift": true, ".scala": true, ".ex": true,
	".exs": true, ".zig": true, ".dart": true, ".vue": true, ".svelte": true,
}

// projectManifests are build/dependency manifests that prove a project exists
// even before any source file does (e.g. a fresh `go mod init`). Matched by
// basename because their extensions (.mod, .json, .toml, none) are useless as
// a signal on their own.
var projectManifests = map[string]bool{
	"go.mod": true, "go.sum": true, "Cargo.toml": true, "package.json": true,
	"pyproject.toml": true, "requirements.txt": true, "pom.xml": true,
	"build.gradle": true, "build.gradle.kts": true, "Gemfile": true,
	"composer.json": true, "CMakeLists.txt": true, "Makefile": true,
}

// errSourceFound is the walk's early-exit sentinel: the first source file
// settles the question, so scanning further is wasted work.
var errSourceFound = errors.New("source file found")

// IsEmptyProject reports whether dir contains no source files — only
// configuration, docs and tooling (.humanconfig.yaml, CLAUDE.md, README.md,
// dotfiles, ...). Dot-directories and dependency/output directories are never
// descended into, so tool state and stray installs cannot mask emptiness or
// fake a project.
func IsEmptyProject(dir string) (bool, error) {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// The root must be readable — otherwise the answer is meaningless.
			// Deeper unreadable entries are tolerated like the codenav walk
			// does: a permission-denied subdir must not break detection.
			if path == dir {
				return humanerrors.WrapWithDetails(walkErr, "reading project directory", "dir", dir)
			}
			return nil
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if isSourceFile(d.Name()) {
			return errSourceFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errSourceFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// isSourceFile decides whether a file proves a project exists: source by
// extension, or a build manifest by basename.
func isSourceFile(name string) bool {
	if projectManifests[name] {
		return true
	}
	return sourceExtensions[strings.ToLower(filepath.Ext(name))]
}
