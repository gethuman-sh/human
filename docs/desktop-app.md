# Desktop App (Wails)

The desktop GUI (`desktop/`) is a [Wails v2](https://wails.io) application: the
interactive workflow board (Ideas → Product backlog → Engineering backlog → Code → Ready to Deploy, plus a terminal Deploy drop zone) delivered for SC-105 / HUM-141. Each card
is a ticket; dragging a card forward triggers that stage's `human`
action through the daemon (Code holds the build-and-review cycle — review
chains automatically after the build — and dropping a reviewed card on Deploy
merges the work after CI passes and closes the ticket), and placement/badges/running-state derive
entirely from the `[human:…]` comment markers (and, for the Ideas queue, the
`human/idea` label) the daemon ships on the wire.

The Ideas queue renders as an **idea space**: one rounded rectangle holding
five invisible, unlabeled lanes. Dragging an idea between lanes sorts it
along the loose→concrete axis (looser left, more concrete right). That placement is the one piece of board
state that is NOT tracker-derived — it is a local workspace preference the
app's Go backend persists to `~/.human/ideaspace.json` (`internal/ideaspace`),
never a label, comment, or status on the ticket. Ideas without a saved
placement sit leftmost, and entries for promoted or closed ideas are pruned
after each successful full fetch.

The whole Go file set under `desktop/` is behind the `wailsapp` build tag, so the
default `go build .` / `go vet ./...` / `go list ./...` / `make check` never
touch the cgo webview path and stay green on a plain toolchain. The desktop
binary is produced only by `wails build` (`make desktop`).

The gating tag is deliberately **not** named `desktop`: Wails reserves `desktop`
as its own output-mode tag and strips it before the host-side binding-generation
build, which would hide every file under `desktop/` and break `wails build` with
"build constraints exclude all Go files". A neutral tag (`wailsapp`) survives
both the binding pass and the final compile; Wails still adds its own `desktop`
tag for cgo backend selection.

## Cross-platform: cgo, no cross-compile

All three Wails backends are cgo — Linux uses webkit2gtk + gtk3, Windows uses
the WebView2 runtime, macOS uses the Obj-C/WebKit toolchain. You therefore
**cannot cross-compile** all OSes from one machine; each target builds on its
own native runner. CI uses a 3-runner matrix (`.github/workflows/desktop.yml`):
`ubuntu-24.04`, `macos-14`, `windows-2022`, each installing its native webview
toolchain.

Wails v2 also guards its `main()` entry point with a build-tag check that is
only satisfied by `wails build` / `wails dev` — **never** plain `go build ./desktop/`.

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

* Wails CLI, pinned: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0` (or `make desktop-deps`)
* Node 20+ (the frontend builds via `tsc` + a dependency-free bundle step)
* Per-OS webview toolchain:
  * **macOS**: Xcode command line tools — `xcode-select --install`
  * **Linux**: `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` (or `-4.0-dev` on older distros)
  * **Windows**: the WebView2 runtime (preinstalled on current Windows images)

## Building

```bash
make desktop
```

`make desktop` wraps `wails build`, which:

* Injects the `desktop`/`production` build tags `desktop/main.go` requires to not panic at startup (our `-tags wailsapp` rides alongside to make the gated files visible).
* On macOS arm64, automatically links the `UniformTypeIdentifiers` framework. A plain `go build` does not add this and fails to link (illustrative; not a guaranteed verbatim linker string):
  ```
  Undefined symbols: _OBJC_CLASS_$_UTType
  ```
* Wraps the binary in a `.app` bundle (using `desktop/build/darwin/Info.plist`) so macOS can launch it as a windowed app.

Manual reproduction of what `wails build` does under the hood (diagnostic only — do not use this as the shipped build path):

```bash
CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
  go build -tags "wailsapp,desktop,production" -o desktop/build/bin/human-desktop ./desktop/
```

Equivalent dev-loop command: `wails dev` (or `make desktop-dev`, if defined).

## Desktop-ticket verification template

Any ticket that touches `desktop/` must state its verification this way:

> Build and smoke-test via `wails build` (`make desktop`) or `wails dev` — never plain `go build ./desktop/`. A plain `go build` links but panics at runtime (see above); a green `go build`/`go test ./desktop/...` is not evidence the app works. Acceptance requires launching the produced `.app` and confirming the window opens and the dashboard attaches to the running daemon.
>
> Additionally, for any change touching project lifecycle: quit the daemon (`human daemon stop`), relaunch the app, and confirm the Projects Overview screen appears (or the last project auto-loads if its directory still exists); pick a project and confirm the board loads; click **Switch Project** and confirm it stops the daemon and returns to the Projects Overview.

## Regression guard

So a future change that breaks `wails build` is caught automatically rather than silently shipping a non-runnable artifact, the following are in place alongside `desktop/`:

1. A comment in `desktop/main.go` directly above the build-tag-guarded `wails.Run` call, explaining that a plain `go build` fails at startup by design and pointing here.
2. `make desktop` runs `wails build -tags wailsapp`; `make desktop-dev` runs `wails dev -tags wailsapp`; `make desktop-package` produces a clean distributable bundle.
3. A CI matrix (`.github/workflows/desktop.yml`) that runs the real `wails build` on all three OSes (`ubuntu-24.04`, `macos-14`, `windows-2022`) so the cgo build path is exercised on every change under `desktop/`. This is a SEPARATE workflow from `ci.yml`; the main lint/test/build jobs deliberately do not install webview headers and rely on the `wailsapp` build tag to keep the cgo path out of `go vet ./...` / `go build .`.

## Release safety

The desktop artifact must never be published through a goreleaser `builds:` entry (e.g. `main: ./desktop`) — that is a plain `go build` and produces the non-runnable binary described above. The artifact is cgo and cannot be cross-compiled, so each OS bundle is produced by `make desktop` (wraps `wails build`) on its native CI runner and, when the artifact ships, attached with goreleaser's `release.extra_files`. See the guard comment in `.goreleaser.yaml`.

## Creating tickets — ideation chat

The Backlog column header shows a '+' button. Clicking it opens a chat-style
panel docked to the right side of the board. (The idea space has its own
lighter '+': it quick-captures a title-only ticket labeled `human/idea` into
its leftmost sub-column, no
chat involved; dragging that idea card onto Backlog opens this same panel in
**evolve mode**, whose terminal action rewrites the idea ticket in place —
title and description replaced, idea label removed, key preserved — instead of
creating a new ticket.) Typing a seed idea starts a
daemon-side ideation agent: the daemon runs headless `claude -p` turns on the
daemon host (`--resume`d per reply, so multi-turn context comes from Claude
Code's own session store), asking one challenge question per turn until it is
confident, then emits a structured `[human:ideation-ticket]` block that the
daemon parses and uses to create the PM ticket via the tracker `Creator`
abstraction, on the single PM-role tracker (resolved by role, the same way the
board's `Cards()` resolves `firstPMResult`). The new card then appears in the
Backlog column through the existing subscribe/refetch loop — no bespoke
refresh path is added for ideation.

The panel talks to three dedicated daemon routes — `ideation-start`,
`ideation-reply`, `ideation-status` — each taking a single JSON argument and
returning the same `IdeationStatus` JSON snapshot, following the
`board-transition` route pattern rather than generic command forwarding.

Lifecycle contract (decided for v1, see HUM-152 AD-4):

* One concurrent session, held in daemon memory.
* Closing the panel does **not** abandon the session — reopening calls
  `ideation-status` and re-attaches to the live transcript.
* Starting a new session while one is already active (not yet `done`/`error`)
  re-attaches to it, idempotently, unless the request sets `restart: true`,
  which abandons the old session and starts a fresh one.
* Sessions do **not** survive a daemon restart (in-memory only).

Requires the `claude` CLI installed and authenticated on the daemon host — if
the binary is missing or the agent turn fails (e.g. not logged in), the panel
surfaces a visible session error rather than hanging.

## macOS code-signing / notarization (release-gating follow-up)

`wails build` does NOT sign or notarize the macOS `.app` — it delegates to Apple's `codesign` / `notarytool` with operator-provided signing identities and secrets. Shipping a notarized macOS build is therefore a release-gating follow-up, not covered by the CI matrix above.
