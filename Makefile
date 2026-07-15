.PHONY: all build fmt fmt-check install test check-test test-integration coverage coverage-check fuzz lint sec secrets check clean upgrade-deps release hooks unhooks desktop desktop-deps desktop-dev desktop-package

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

build:
	go build -ldflags "-X main.version=dev -X main.commit=$$(git rev-parse --short HEAD) -X main.date=$$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o bin/human .

install:
	go install .

test:
	go tool gotestsum ./...

# check-test is the pre-push test gate. It runs the suite fresh (-count=1) so a
# stale go test cache can never mask a failure — the cached `test` target above
# stays fast for local iteration, but `check` must not trust it. Scoped to CI's
# package set (excluding /cmd/). The coverage threshold is intentionally NOT
# enforced here: it is environment-sensitive (fuse-backed tests skip without
# fuse3 installed, under-reporting locally) and is enforced by CI instead.
check-test:
	go tool gotestsum -- -count=1 $$(go list ./... | grep -v /cmd/)

coverage:
	go tool gotestsum -- -coverprofile=coverage.out $$(go list ./... | grep -v /cmd/)
	go tool cover -func=coverage.out

coverage-check: coverage
	@go tool cover -func=coverage.out | awk '/^total:/{gsub(/%/,"",$$NF); printf "Total coverage: %s%%\n", $$NF; if ($$NF+0 < 80.0) {print "FAIL: below 80% threshold"; exit 1} else {print "OK: meets 80% threshold"}}'

fuzz:
	go test -run=^$$ -fuzz=FuzzSanitizeFTSQuery -fuzztime=30s ./internal/index/...
	go test -run=^$$ -fuzz=FuzzPeekClientHello -fuzztime=30s ./internal/proxy/...

lint:
	go vet ./...
	go tool staticcheck ./...
	go tool golangci-lint run ./...
	go tool nilaway ./...
	go tool gocyclo -over 15 .

sec:
	go tool gosec ./...
	./scripts/govulncheck.sh

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
	go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0

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
	cd desktop && wails build -tags $(DESKTOP_TAGS)

# desktop-dev runs the live-reload dev loop.
desktop-dev:
	cd desktop && wails dev -tags $(DESKTOP_TAGS)

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
