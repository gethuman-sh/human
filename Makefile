.PHONY: all build build-linux fmt fmt-check install test check-test test-integration coverage coverage-check fuzz fsm fsm-diagram lint sec secrets check clean upgrade-deps release hooks unhooks desktop desktop-deps desktop-dev desktop-package desktop-frontend desktop-frontend-check

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")

# Formatting is NOT part of `build`: goimports needs full import resolution and
# costs ~1 minute even on tracked files only (a bare `goimports -w .` also walks
# node_modules and runs for several minutes). Run `make fmt` to fix imports;
# `check` runs the non-destructive fmt-check gate so drift can't reach a push.
# Scoped to git-tracked files to skip vendored and generated trees.
fmt:
	go tool goimports -w $$(git ls-files '*.go')

fmt-check:
	@unformatted=$$(go tool goimports -l $$(git ls-files '*.go')); if [ -n "$$unformatted" ]; then echo "unformatted files (run 'make fmt'):"; echo "$$unformatted"; exit 1; fi

# runtime/secret erases the working copies made while reading a credential
# (SC-2183). It is experimental, so it is selected by build tag: without this
# the build compiles a stub and behaves exactly as before. Release builds set
# the same variable (.goreleaser.yaml) so a shipped binary is not less protected
# than a locally built one. Override with `make build GOEXPERIMENT=` to opt out.
#
# Enabling this used to segfault the daemon: the erasure fought the WebAssembly
# runtime inside the 1Password SDK. That SDK is gone, and with it the only wasm
# in this binary — check `go version -m` before assuming it is safe to add back.
GOEXPERIMENT ?= runtimesecret
export GOEXPERIMENT

# Stamped into both `build` and `build-linux` so the binary a container runs
# reports the same version as the host's — the reason for cross-building one at
# all. Recursively expanded, and the $$ reaches the recipe's shell, so the
# commit and date are read when a target runs rather than at parse time.
LDFLAGS = -X main.version=dev -X main.commit=$$(git rev-parse --short HEAD) -X main.date=$$(date -u +%Y-%m-%dT%H:%M:%SZ)

# build-linux rides along because the artifact is load-bearing: a launch copies
# bin/human-linux-$(LINUX_ARCH) into the container, and building the two apart is
# how their version stamps drift (SC-4631).
build: build-linux
	go build -ldflags "$(LDFLAGS)" -o bin/human .

# The agent container runs linux, so a macOS or Windows host cannot pin the
# container to its own build by binding its binary in — the executable format
# differs and every in-container `human` answers "exec format error" (SC-4596).
# Falling back to the binary the devcontainer feature shipped is not the same
# thing: that one lags the host by weeks and lacks the pipeline commands an
# agent needs to record its work. build-linux produces a binary the container
# can execute, carrying the host's own version stamp.
#
# CGO is off so the result runs against whatever libc the image has; nothing on
# the CLI path needs it. LINUX_ARCH defaults to the host's architecture, which
# is the container's too under Docker Desktop — override it to build the other.
LINUX_ARCH ?= $(shell go env GOARCH)

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -ldflags "$(LDFLAGS)" -o bin/human-linux-$(LINUX_ARCH) .

# install delivers both user-facing artifacts: the CLI via go install, and the
# board app next to it in the Go bin dir (or into ~/Applications as a .app
# bundle on macOS, where a bare binary is not a launchable app). Building the
# board requires the Wails CLI — `make desktop-deps`.
install: desktop
	go install .
	@if [ -d desktop/build/bin/human-board.app ]; then \
		mkdir -p "$$HOME/Applications" && \
		rm -rf "$$HOME/Applications/human-board.app" && \
		cp -R desktop/build/bin/human-board.app "$$HOME/Applications/" && \
		echo "human-board.app -> $$HOME/Applications"; \
	else \
		bindir="$$(go env GOBIN)"; [ -n "$$bindir" ] || bindir="$$(go env GOPATH)/bin"; \
		install -m 0755 desktop/build/bin/human-board* "$$bindir/" && \
		echo "human-board -> $$bindir"; \
	fi

test:
	go tool gotestsum ./...

# GATE_PKGS is the package set EVERY test gate runs — the pre-push one below and
# the CI one, which invokes these targets rather than restating them. It is one
# variable because the bug it fixes was one filter living in three copies: from
# 0f23ff06 (SC-2173, where the CI workflow was incidental) a `go list` piped
# through a filter that dropped every /cmd/ package sat in check-test, in
# coverage and in ci.yml, with no commit, comment or ticket recording a reason,
# and 88 _test.go files / 17,510 lines
# under cmd/ — the daemon command layer, the board driver, the deploy path, the
# stage-retry logic, the PR loop — ran in no gate that could fail a push or a
# merge. Two cmd/cmddaemon assertions were red on main for five days behind it
# and nothing could see them (SC-3877).
#
# Narrowing this narrows every gate at once, which is the point: a filter must
# not be able to live in one gate and not the other. Override it on the command
# line to run a subset locally; do not edit the default without saying why here.
#
# Cost of the cmd/ packages, measured 2026-09-25 on this tree: `make check-test`
# went 65.6s -> 66.9s (+1.3s), second of two warm-cache runs each way.
GATE_PKGS ?= ./...

# COVERAGE_EXCLUDE is a grep pattern; profile lines matching it are held out of
# the 80% threshold below. It scopes the DENOMINATOR ONLY — every package in
# GATE_PKGS still runs, in every gate — and coverage-check prints the exclusion
# on every run, so a passing gate can never quietly mean "measured less than it
# looks like". Empty means nothing is excluded.
#
# cmd/ sits here because folding it into the profile measured 76.3% total
# against the 80% floor on 2026-09-25 (cmd/ alone: 52.1%) — the threshold would
# go red on the very commit that closed the gate's hole. The tests RUN; only
# the denominator is scoped, and excluding these lines reproduces the
# pre-change number exactly (84.0%), because without -coverpkg a package's
# coverage comes only from its own tests. Raising cmd/ coverage and emptying
# this is the follow-up (SC-3877).
COVERAGE_EXCLUDE ?= /cmd/

# check-test is the pre-push test gate. It runs the suite fresh (-count=1) so a
# stale go test cache can never mask a failure — the cached `test` target above
# stays fast for local iteration, but `check` must not trust it. The coverage
# threshold is intentionally NOT enforced here: it is environment-sensitive
# (fuse-backed tests skip without fuse3 installed, under-reporting locally) and
# is enforced by CI instead. The package set it runs is echoed first, so the
# gate's own output states what it covered.
check-test:
	@echo "test package set: $(GATE_PKGS)"
	go tool gotestsum -- -count=1 $(GATE_PKGS)

coverage:
	@echo "coverage package set: $(GATE_PKGS)"
	go tool gotestsum -- -coverprofile=coverage.out $(GATE_PKGS)
	go tool cover -func=coverage.out

coverage-check: coverage
	@if [ -n "$(COVERAGE_EXCLUDE)" ]; then \
		echo "coverage threshold: EXCLUDING profile lines matching '$(COVERAGE_EXCLUDE)' — those packages still run in every gate; the reason is at COVERAGE_EXCLUDE in the Makefile"; \
		grep -v -- '$(COVERAGE_EXCLUDE)' coverage.out > coverage-threshold.out || true; \
	else \
		echo "coverage threshold: all of $(GATE_PKGS), nothing excluded"; \
		cp coverage.out coverage-threshold.out; \
	fi
	@test "$$(wc -l < coverage-threshold.out)" -gt 1 || { echo "FAIL: the coverage profile is empty after exclusion — the threshold would pass by measuring nothing"; exit 1; }
	@go tool cover -func=coverage-threshold.out | awk '/^total:/{gsub(/%/,"",$$NF); printf "Total coverage: %s%%\n", $$NF; if ($$NF+0 < 80.0) {print "FAIL: below 80% threshold"; exit 1} else {print "OK: meets 80% threshold"}}'

fuzz:
	go test -run=^$$ -fuzz=FuzzSanitizeFTSQuery -fuzztime=30s ./internal/index/...
	go test -run=^$$ -fuzz=FuzzPeekClientHello -fuzztime=30s ./internal/proxy/...

# fsm validates internal/pipelinefsm/pipeline-fsm.json as a machine: dangling destinations,
# unreachable states, states with no way out, undeclared actors. `check` already
# fails on those through internal/pipelinefsm's own test — this target is for
# reading the whole list at once while fixing it, warnings included.
# fsm-diagram draws the machine for a PR or a doc.
fsm:
	go run ./cmd/fsmcheck

fsm-diagram:
	go run ./cmd/fsmcheck -mermaid

# golangci-lint's default set already includes govet and the full staticcheck
# suite, so the standalone tools would run the same analyses twice.
lint:
	go tool golangci-lint run ./...
	go tool gocyclo -over 15 .

sec:
	# .claude/worktrees holds agent worktrees — stale snapshots there must not
	# be compiled against the live tree (a changed interface fails the scan).
	go tool gosec -exclude-dir .claude ./...
	go tool govulncheck ./...

secrets:
	go tool gitleaks git -v

test-integration: build
	go run ./cmd/integrationtest

check: fmt-check check-test lint sec secrets

# Desktop (Wails) targets. The desktop app is a cgo backend (webkit2gtk on
# Linux, WebView2 on Windows, Obj-C on macOS) and CANNOT be cross-compiled — it
# is built on its native runner only (see .github/workflows/desktop.yml). All
# desktop Go files are behind the `wailsapp` build tag so the default `build`,
# `test`, `lint` and `check` targets never touch this cgo path and stay green on
# a plain toolchain. The tag is NOT `desktop`: Wails reserves that name and
# strips it before binding generation, which would hide every file. Building
# requires the Wails CLI (desktop-deps installs it).
desktop-deps:
	go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0

# Wails v2 defaults to the EOL webkit2gtk-4.0 ABI on Linux; modern Debian/Ubuntu
# ship only webkit2gtk-4.1, which requires the `webkit2_41` tag. Auto-append it
# when 4.0 is absent but 4.1 is present so the build works out of the box; macOS
# and Windows are untouched. Override the whole set with `make desktop DESKTOP_TAGS=...`.
DESKTOP_TAGS ?= wailsapp$(shell pkg-config --exists webkit2gtk-4.0 2>/dev/null || { pkg-config --exists webkit2gtk-4.1 2>/dev/null && echo ,webkit2_41; })

# desktop produces a runnable app for the CURRENT OS only. wails build invokes
# the frontend build (tsc + bundle) and compiles the cgo backend; Wails adds its
# own `desktop` output tag, and `-tags wailsapp` makes our gated files visible to
# both the binding-generation pass and the final compile. A plain
# `go build ./desktop/` links but panics at startup, so it is never the build
# path (see docs/desktop-app.md).
desktop:
	@command -v wails >/dev/null || { echo "error: wails CLI not found — run 'make desktop-deps'"; exit 1; }
	cd desktop && wails build -tags $(DESKTOP_TAGS)

# desktop-dev runs the live-reload dev loop.
desktop-dev:
	cd desktop && wails dev -tags $(DESKTOP_TAGS)

# The frontend half of the desktop build, on its own: `wails build` runs it as a
# sub-step, but dist/ is checked in and embedded, so a src/ edit that never got
# rebuilt ships stale (SC-3613). desktop-frontend-check runs the same comparison
# CI runs, so drift is visible before a push instead of only after one.
# Deliberately NOT part of `check`: that gate runs where npm does not exist.
desktop-frontend:
	cd desktop/frontend && npm ci && npm run build

desktop-frontend-check: desktop-frontend
	cd desktop/frontend && node scripts/dist-guard.mjs

# desktop-package produces a clean distributable bundle (.app/.exe/AppImage) for
# the current OS. Note: macOS code-signing/notarization is NOT performed here —
# wails delegates to Apple codesign/notarytool with operator-provided
# identities; that remains a release-gating follow-up.
desktop-package:
	cd desktop && wails build -tags $(DESKTOP_TAGS) -clean

clean:
	go clean -cache -i

all: lint sec build

upgrade-deps:
	go get -u ./...
	go mod tidy
	go tool gotestsum ./...

tokens:
	@find . -name '*.go' ! -path './vendor/*' -exec cat {} + | wc -w | awk '{printf "%d words (~%d tokens)\n", $$1, int($$1 * 1.3)}'

hooks:
	git config core.hooksPath .githooks

unhooks:
	git config --unset core.hooksPath

release:
	@test -z "$$(git status --porcelain)" || (echo "error: working tree is dirty" && exit 1)
	@echo "Tagging $(VERSION)..."
	git tag -a $(VERSION) -m "Release $(VERSION)"
	git push origin $(VERSION)
	# goreleaser needs a token to publish the release and push the Homebrew tap;
	# fall back to the logged-in gh CLI so a plain `make release` works locally.
	GITHUB_TOKEN="$${GITHUB_TOKEN:-$$(gh auth token)}" go tool goreleaser release --clean
