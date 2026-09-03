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
own native runner. CI uses a 2-runner matrix (`.github/workflows/desktop.yml`):
`ubuntu-24.04` and `macos-14`, each installing its native webview
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

* Wails CLI, pinned: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0` (or `make desktop-deps`)
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

### The frontend bundle (`dist/`)

`desktop/frontend/dist/` is **checked in** and embedded via `//go:embed all:frontend/dist`, so the app runs without an npm build — which also means a `src/` edit that was never rebuilt ships stale. After editing anything under `desktop/frontend/src/` or `static/`:

```bash
make desktop-frontend        # npm ci && npm run build — regenerates dist/
make desktop-frontend-check  # the same drift check CI runs
```

Then commit the changed `dist/` files with your source change. Never hand-edit `dist/`: it is build output, and a hand-transcribed bundle is what broke every deploy for a day (SC-3613).

The check compares the committed bundle against a fresh build **after normalizing whole-line whitespace** — line endings, indentation and trailing spaces. Two builds of identical source may differ that way, and blocking a merge on it is not a real failure; anything else, including a changed string literal, still fails. Neither target is part of `make check`, which runs where npm is unavailable.

## Exiting the app

The app has two exit paths, and they answer to different people.

**Closing the window** goes through the confirmation flow (`desktop/closeflow.go`): an idle daemon is stopped silently, a busy one raises the three-way dialog (Cancel / Stop anyway / Wait and close), and the app clears its session marker so a cleanly stopped daemon is never later reported orphaned.

**Ctrl-C (or SIGTERM) in the terminal** ends the process immediately and leaves the daemon running (`desktop/signalexit.go`). It performs no busy check, shows no dialog, and never stops the daemon: whoever starts the app from a console manages their own daemon. A dialog inside the window is no answer to a question asked from a shell — and Wails' own signal handler replaces Go's default terminate and routes signals into `OnBeforeClose`, so without this the app could not be ended from the terminal it was started in at all.

## Desktop-ticket verification template

Any ticket that touches `desktop/` must state its verification this way:

> Build and smoke-test via `wails build` (`make desktop`) or `wails dev` — never plain `go build ./desktop/`. A plain `go build` links but panics at runtime (see above); a green `go build`/`go test ./desktop/...` is not evidence the app works. Acceptance requires launching the produced `.app` and confirming the window opens and the dashboard attaches to the running daemon.
>
> Additionally, for any change touching project lifecycle: quit the daemon (`human daemon stop`), relaunch the app, and confirm the Projects Overview screen appears (or the last project auto-loads if its directory still exists); pick a project and confirm the board loads; click **Switch Project** and confirm it stops the daemon and returns to the Projects Overview.
>
> For SC-3015 (close/orphan behavior): with the daemon idle, close the window and confirm the daemon process actually exits (`human daemon status`) with no dialog; start an agent run (or hold a stage lease) and close the window, confirming the three-way dialog appears, and that Cancel/Stop anyway/Wait and close each behave as described; force-quit the app (not the normal close) while its daemon is still running, then relaunch and confirm the orphan-cleanup prompt appears and both its choices (stop it / leave it running) behave correctly; separately, start a daemon manually via `human daemon start` with no app attached, launch the app, and confirm NO orphan prompt appears for it.
>
> For SC-3292 (console exit): launch from a terminal (`make desktop-dev`, or run the built binary directly), press Ctrl-C, and confirm the process ends at once — with an agent run in flight as well as idle, with no dialog either time — and that `human daemon status` shows the daemon still running afterwards. Relaunch and confirm NO orphan prompt appears for that daemon.
>
> For SC-3346 (cwd-based auto-open): with no daemon running, launch the app from a terminal `cd`ed into a project directory containing a valid `.humanconfig.yaml`, and confirm that project's board opens automatically with no manual "open project" step. With that daemon still running, quit and relaunch the app from the same directory and confirm it lands on the same session with no restart (`human daemon status` reports the same PID throughout). From a DIFFERENT project directory, relaunch and confirm the app shows the still-running (unrelated) project's board plus a one-button notice naming both projects, and that the running daemon's own project is untouched. Break that directory's `.humanconfig.yaml` (invalid YAML), relaunch, and confirm the Projects Overview screen appears with a distinct "project config ... is invalid" error rather than silently falling back to the last-opened project. Finally, launch from a directory with no config file at all and confirm behavior is unchanged from before this ticket (reachable-daemon / last-recent-project / Projects Overview).

## Regression guard

So a future change that breaks `wails build` is caught automatically rather than silently shipping a non-runnable artifact, the following are in place alongside `desktop/`:

1. A comment in `desktop/main.go` directly above the build-tag-guarded `wails.Run` call, explaining that a plain `go build` fails at startup by design and pointing here.
2. `make desktop` runs `wails build -tags wailsapp`; `make desktop-dev` runs `wails dev -tags wailsapp`; `make desktop-package` produces a clean distributable bundle.
3. A CI matrix (`.github/workflows/desktop.yml`) that runs the real `wails build` on `ubuntu-24.04` and `macos-14` so the cgo build path is exercised on every change under `desktop/`. Windows is not built: its runner took ~8 minutes and gated every desktop merge, and no Windows desktop bundle ships today — restore the row before one does. This is a SEPARATE workflow from `ci.yml`; the main lint/test/build jobs deliberately do not install webview headers and rely on the `wailsapp` build tag to keep the cgo path out of `go vet ./...` / `go build .`.
4. `desktop/frontend/scripts/dist-guard.mjs`, run by the `frontend-test` job and by `make desktop-frontend-check`: it rebuilds nothing on its own but compares the committed `dist/` against the freshly built one, so a bundle whose behaviour lags `src/` cannot ship. It is deliberately not a byte comparison — see the section above.

## Release safety

The desktop artifact must never be published through a goreleaser `builds:` entry (e.g. `main: ./desktop`) — that is a plain `go build` and produces the non-runnable binary described above. The artifact is cgo and cannot be cross-compiled, so each OS bundle is produced by `make desktop` (wraps `wails build`) on its native CI runner and, when the artifact ships, attached with goreleaser's `release.extra_files`. See the guard comment in `.goreleaser.yaml`.

## Creating tickets — capture, draft, promote (SC-4608)

There is one way into ticket creation and it is a text field: the idea
space's "Capture an idea" button quick-captures a title-only ticket
labeled `human/idea` into the leftmost sub-column. The
post-project-import "Create first ticket" prompt opens that same
quick-add — a first ticket is an idea like every other one. (SC-4485 had
already removed the Backlog column's own '+' and the left rail's "new
ticket" action; SC-4725 then gave the surviving control the accent fill
and the written label it carries today, since the one action that starts
all work should not read as chrome.)

What used to be an interview at promotion time is now background work
while the idea sits in Ideas. Capture fires a containerized
`idea-draft-<KEY>` run that writes **the description** — never the title —
leaving an inline `[TBA: <question>]` wherever it would have had to guess;
the Ideas card face shows the count of unanswered ones. It writes only over
its own words: a `[human:idea-draft]` marker records the fingerprint of what
it wrote, and once a person edits the description nothing automatic ever
writes to that ticket again. An idea **retitled** outside the board is
redrafted via the board freshness poll's `UpdatedAt` diff, debounced. The
title is the trigger because it is the input a draft is made from, and
because the drafter's own write advances `UpdatedAt` — redrafting on any
advance would relaunch the run that write came from, on a loop. Editing an
idea's description outside the board therefore raises no redraft: those are
words the overwrite guard stands down on anyway.

Dragging an Ideas card onto Product Backlog **promotes** it: the
idea-classifying labels come off through the `idea-promote` route — no
agent turn, no rewrite, same key, same title — and the board opens the
description editor (below) on the drafted text, with its remit widened to
challenge scope and work through the `[TBA: …]` questions. The card lands
in Product Backlog on the next refetch.

The ideation chat panel that used to do this is **unreachable**: SC-4608
retired the guided interview, its approval park, the drag-to-Backlog
ideation launch and the evolve mode whose terminal action rewrote an idea
ticket in place. The daemon engine behind it — the `ideation-start`,
`ideation-reply` and `ideation-status` routes, the session store, and the
direct-create path (`creator.CreateIssue`) the CLI still uses — is left
standing for the follow-on ticket to remove, so the interactive flow can be
restored from a wire rather than from git if the drafts turn out thin.
CLI-side ideation (`/human-ideate`, the `human-ideator` agent,
`human idea promote`) is untouched and still works.

Drafting requires the container substrate and the `claude` CLI on the daemon
host. With neither, capture still returns and promotion still works — the
description is simply empty, and the editor opens on it.

## Refining a description — Product Backlog chat editor (SC-2873)

Clicking a card in the Product Backlog lane opens a centered split-view
**modal** — description on the left, a scoped chat on the right — instead of
the read-only slide-out detail panel every other lane still opens (the click
routing guard is `queueOf(card) === "product"`, matching the lane resolution
`board-queue.ts` already uses elsewhere, not the raw wire stage). This is a
deliberate, narrow first step (see the ticket's Known Limitation): only the
Product Backlog lane's click target changed.

The chat is scoped to rewriting the description text only — it declines to
discuss title, acceptance criteria, labels or any other field. Opened on a
**just-promoted** card its remit is wider (SC-4608): it may challenge the
premise and the scope, and it works through the drafter's `[TBA: …]`
questions with the user — never answering one itself, never deleting one the
user has not answered. It still proposes description text and nothing else.
A rewrite the agent proposes appears in the left pane as a visibly distinct
"Proposed rewrite (unsaved)" preview; nothing reaches the tracker until the user clicks
Apply. Applying also records the description as the human's words, so the
background drafter never writes over an applied edit. Closing this modal without
Apply/Save **discards the daemon-side chat session outright** (AC6): the
modal's close path (Close button, Escape, backdrop click) calls
`descedit-discard` on whatever session was live, so reopening the SAME ticket
always starts a genuinely fresh session — no stale proposal, no stale chat
history. A close that races the opening `descedit-start` is discarded on the
same route: the session exists on the daemon before its id ever reaches the
UI, so the open path re-checks which ticket the modal still belongs to when
`start` returns and discards a session no modal owns, rather than adopting it.
The session is also **not** persisted across a daemon restart (an
in-progress, unsaved edit is cheap to lose since nothing was ever written).

The panel talks to five dedicated daemon routes — `descedit-start`,
`descedit-reply`, `descedit-apply`, `descedit-discard`, `descedit-status` —
following the same single-JSON-argument route pattern as the ideation routes
above. `Apply` writes through the role-resolved PM tracker's `Editor`,
touching only the `Description` field of `tracker.EditOptions` — `Title` and
label fields are never set, so the write path cannot drift into editing
anything else. `Discard` never touches the tracker either; it only ends the
in-memory session so `Start`'s same-key reattach (used within a single
still-open modal instance, e.g. a retried `Start` call) has nothing stale left
to reattach to after a close.

## macOS code-signing / notarization (release-gating follow-up)

`wails build` does NOT sign or notarize the macOS `.app` — it delegates to Apple's `codesign` / `notarytool` with operator-provided signing identities and secrets. Shipping a notarized macOS build is therefore a release-gating follow-up, not covered by the CI matrix above.
