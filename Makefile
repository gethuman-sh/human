.PHONY: all build install test check-test test-integration coverage coverage-check fuzz lint sec secrets check clean upgrade-deps release hooks unhooks

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")

build:
	go tool goimports -w .
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

check: check-test lint sec secrets

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
	go tool goreleaser release --clean
