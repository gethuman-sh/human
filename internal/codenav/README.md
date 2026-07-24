# Code Navigation

`human codenav` indexes a repository into a local SQLite database and answers structural questions about it — go-to-definition, find-references, call graphs, blast radius, and full-text search — fast, offline, and token-frugal, so an AI agent can navigate code without dumping whole files into context. It runs locally where the code is; when a `human` daemon is running it instead serves a shared index that every worktree and agent container queries, so navigation is indexed once per repository rather than once per checkout.

- Indexes Go precisely and other languages structurally
- Go-to-definition and find-references with exact locations
- Walks call graphs: callers, callees, and call paths
- Reports blast radius of a symbol or a git diff
- Full-text search over code bodies and symbol names
- Lists detected web routes and their handlers
- Keeps one local index for many repositories
- Refreshes incrementally — reprocesses only added, modified, and deleted files (Go at package granularity) and leaves unchanged files in place, so staying current is cheap; `--full` forces a complete rebuild. The call graph is exact except for interface-dispatch edges between full rebuilds.
- Served by the daemon as one shared index for every agent and worktree, kept fresh in the background; falls back to a local index when no daemon runs
