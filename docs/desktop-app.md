# Desktop App (Wails)

The desktop GUI (`desktop/`, delivered by HUM-106) is a [Wails v2](https://wails.io) application. Wails v2 guards its `main()` entry point with a build-tag check that is only satisfied by `wails build` / `wails dev` — **never** plain `go build ./desktop/`.

## Why plain `go build` is not a valid smoke test

`go build ./desktop/` (or `go test ./desktop/...`) only proves the Go source type-checks. It does **not** produce a runnable app:

```
panic: Wails applications will not build without the correct build tags.
    main.main()
    desktop/main.go:46
```

(Whether this surfaces as a literal `panic:` or a printed error followed by exit depends on how `desktop/main.go` handles the error Wails returns from `CreateApp`/equivalent — either way, the app **fails at startup**.)

The only valid acceptance signal for the desktop app is launching the built `.app` and confirming the window opens and the dashboard attaches to the running daemon.

## Toolchain prerequisites

* Wails CLI, pinned: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0`
* Xcode command line tools: `xcode-select --install`

## Building

```bash
make desktop
```

`make desktop` wraps `wails build`, which:

* Injects the `desktop`/`production` build tags `desktop/main.go` requires to not panic at startup.
* On macOS arm64, automatically links the `UniformTypeIdentifiers` framework. A plain `go build` does not add this and fails to link (illustrative; not a guaranteed verbatim linker string):
  ```
  Undefined symbols: _OBJC_CLASS_$_UTType
  ```
* Wraps the binary in a `.app` bundle (using `desktop/build/darwin/Info.plist`) so macOS can launch it as a windowed app.

Manual reproduction of what `wails build` does under the hood (diagnostic only — do not use this as the shipped build path):

```bash
CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
  go build -tags "desktop,production" -o desktop/build/bin/human-desktop ./desktop/
```

Equivalent dev-loop command: `wails dev` (or `make desktop-dev`, if defined).

## Desktop-ticket verification template

Any ticket that touches `desktop/` must state its verification this way:

> Build and smoke-test via `wails build` (`make desktop`) or `wails dev` — never plain `go build ./desktop/`. A plain `go build` links but panics at runtime (see above); a green `go build`/`go test ./desktop/...` is not evidence the app works. Acceptance requires launching the produced `.app` and confirming the window opens and the dashboard attaches to the running daemon.

## Regression guard

So a future change that breaks `wails build` is caught automatically rather than silently shipping a non-runnable artifact, HUM-106 must add, alongside `desktop/`:

1. A comment in `desktop/main.go` directly above the build-tag-guarded `main()`/`CreateApp` call, explaining that a plain `go build` panics by design and pointing here.
2. A `make desktop` target in the `Makefile` that runs `wails build` (and `make desktop-dev` for `wails dev`, optionally).
3. `scripts/build-desktop.sh`, mirroring the existing release-script conventions, that:
   * Exits 0 without building when `uname -s` is not `Darwin`, or the `wails` binary is not on `PATH` (clean skip, not a failure) — desktop builds are macOS-only today.
   * Otherwise runs `wails build` and reports the resulting `.app` path.
4. A CI lane that runs `make desktop` on a macOS runner so the real `wails build` path is exercised at least once per release. Candidate runner label: `runs-on: macos-14` — **UNVERIFIED, confirm against the current [GitHub-hosted runner docs](https://docs.github.com/en/actions/using-github-hosted-runners/about-github-hosted-runners/about-github-hosted-runners#supported-runners-and-hardware-resources) before wiring this in.**

## Release safety

The desktop artifact must never be published through a goreleaser `builds:` entry (e.g. `main: ./desktop`) — that is a plain `go build` and produces the panicking binary described above. When the desktop artifact ships, it must be built via `scripts/build-desktop.sh` (which runs `wails build`) and attached with goreleaser's `release.extra_files`. See the guard comment in `.goreleaser.yaml`.
