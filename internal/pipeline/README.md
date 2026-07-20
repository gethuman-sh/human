# Pipeline Runtime

Shared bookkeeping for the multi-agent scan pipelines (findbugs, security, gardening, brainstorm), surfaced as `human pipeline`. Finder agents run in parallel and contribute judgment; this runtime owns the mechanics they used to hand-roll per prompt.

- Creates the pipeline workspace under `.human/<name>/` (`human pipeline init`)
- Appends findings with race-free sequential IDs — parallel agents can no longer collide on C-NNN allocation (`human pipeline append`)
- Drops exact duplicates mechanically (same file, line, and category); same-root-cause merging stays with triage judgment
- Counts candidates (`human pipeline count`) — replaces per-agent count files
- Shared key-value state for iteration bookkeeping (`human pipeline state get|set`)
- Hands out the timestamped final-report path (`human pipeline report`)
- Cleans up all intermediate dot-files while keeping final reports (`human pipeline cleanup`)
