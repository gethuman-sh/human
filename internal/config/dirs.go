package config

import (
	"context"

	"github.com/gethuman-sh/human/internal/env"
)

const (
	// DirCwd means "the caller's working directory". Use for direct CLI
	// invocations where the user's cwd is the intended config location.
	DirCwd = "."

	// DirProject means "the project directory for this request". Inside the
	// daemon this resolves to the registered project directory via the
	// per-request env map carried on the cobra command context. Outside
	// the daemon it falls back to ".".
	DirProject = "@project"
)

// ResolveDir maps dir sentinel values to real paths using the process
// environment. Prefer ResolveDirCtx in code that runs inside the daemon
// — see env.Lookup for the rationale.
func ResolveDir(dir string) string {
	return ResolveDirCtx(context.Background(), dir)
}

// ResolveDirCtx maps dir sentinel values to real paths, consulting the
// per-request env map on ctx before falling back to the process env.
// Daemon-served handlers MUST use this variant; cross-request env
// contamination is otherwise possible.
func ResolveDirCtx(ctx context.Context, dir string) string {
	if dir == DirProject {
		if d := env.Lookup(ctx, "HUMAN_PROJECT_DIR"); d != "" {
			return d
		}
		return "."
	}
	return dir
}
