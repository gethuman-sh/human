# Project Starters

Detects directories that contain no project yet — only tool configuration like
`.humanconfig.yaml`, `CLAUDE.md`, or `.claude/` — and scaffolds them from
curated starter templates. The desktop app uses this to offer a Start Project
wizard on an empty board instead of leaving new users staring at nothing.

Templates live in the public [gethuman-sh/starters](https://github.com/gethuman-sh/starters)
repository, one per `<type>/<language>/` subdirectory (v1 ships `web/go`, a
minimal Go web server). The wizard's choices are generated from the template
registry, so new project types and languages are registry additions — no UI
changes required.

## Capabilities

- **Empty-project detection** — walks the directory and reports whether any
  source file or build manifest exists; dot-directories and dependency/output
  directories (`node_modules`, `vendor`, `dist`, …) never count.
- **Template registry** — the available starters with display labels for the
  wizard's type and language steps.
- **Safe scaffolding** — downloads the starters tarball from GitHub and
  extracts only the chosen template into the project directory: existing files
  are never overwritten, archive paths are validated against traversal,
  symlinks are dropped, and decompression is size-capped.
