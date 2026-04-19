# lapp project quality gate
# Single source of truth for "my code is clean" — hooks and CI delegate here.

# Allow Go to download newer toolchains as needed (spec requires Go 1.24+,
# dev tools like golangci-lint need 1.25+). GOTOOLCHAIN=auto lets Go manage this.
export GOTOOLCHAIN := "auto"

# Developer tools are pinned in tools.mod and invoked through go tool.
go_tool := "go tool -modfile=tools.mod"

version := `git describe --tags --always --dirty 2>/dev/null || echo "dev"`
ldflags := "-X main.Version=" + version + " -X github.com/lapp-dev/lapp/internal/server.Version=" + version

default:
    @just --list

# --- Quality gates ---

# Pre-commit: fast local checks + fresh non-race tests
pre-commit: fmt vet lint build-check mod-tidy betterleaks test-fast

# Pre-push: pre-commit checks + race tests + vulnerability scan
pre-push: pre-commit test-race vuln

# Full quality gate: same checks as pre-push
check: pre-push

# Full dev suite: quality gate + coverage
dev: check cover
    @echo "All checks passed!"

# --- Static analysis ---

# Check formatting with gofumpt (detect-only, no auto-fix)
fmt:
    @test -z "$({{go_tool}} gofumpt --extra -l .)" || (echo "gofumpt: unformatted files:" && {{go_tool}} gofumpt --extra -l . && exit 1)

# Go vet
vet:
    go vet ./...

# Lint with golangci-lint
lint:
    {{go_tool}} golangci-lint run

# --- Security ---

# Scan for leaked secrets
betterleaks:
    @if command -v betterleaks >/dev/null 2>&1; then \
        betterleaks git --no-banner; \
    else \
        echo "warning: betterleaks not installed, skipping secret scan"; \
    fi

# Scan for known vulnerabilities in dependencies
vuln:
    {{go_tool}} govulncheck ./...

# --- Testing ---

# Verify the project compiles (fast, no binary output)
build-check:
    go build ./...

# Verify go.mod and go.sum are tidy (detect-only)
mod-tidy:
    @cp go.mod go.mod.bak
    @if [ -f go.sum ]; then cp go.sum go.sum.bak; fi
    @go mod tidy
    @DIRTY=0; \
        diff -q go.mod go.mod.bak >/dev/null 2>&1 || DIRTY=1; \
        if [ -f go.sum.bak ]; then diff -q go.sum go.sum.bak >/dev/null 2>&1 || DIRTY=1; \
        elif [ -f go.sum ]; then DIRTY=1; fi; \
        mv go.mod.bak go.mod; \
        if [ -f go.sum.bak ]; then mv go.sum.bak go.sum; elif [ -f go.sum ]; then rm go.sum; fi; \
        if [ "$$DIRTY" = "1" ]; then echo "go.mod/go.sum not tidy — run 'go mod tidy'" && exit 1; fi

# Run all tests without race detector (fresh)
test: test-fast

# Run all tests without race detector (fresh)
test-fast:
    go test -count=1 ./...

# Run all tests with race detector (fresh)
test-race:
    go test -race -count=1 ./...

# Run tests with coverage report
cover:
    go test -race -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# --- Build targets ---

# Build the lapp binary with version info
build:
    mkdir -p bin
    go build -ldflags '{{ldflags}}' -o bin/lapp ./cmd/lapp

# Install lapp to $GOPATH/bin (or $GOBIN)
install:
    go install -ldflags '{{ldflags}}' ./cmd/lapp

# --- Setup ---

# Format all Go files in-place (use when `just fmt` fails)
format:
    {{go_tool}} gofumpt --extra -w .

# Set up git hooks and development environment
setup: install-dev
    @mkdir -p .git/hooks
    @cp -f scripts/pre-commit .git/hooks/pre-commit 2>/dev/null || true
    @cp -f scripts/pre-push .git/hooks/pre-push 2>/dev/null || true
    @chmod +x .git/hooks/pre-commit .git/hooks/pre-push 2>/dev/null || true
    @echo "Git hooks installed (.git/hooks/)"

# Cache required development tools (pinned in tools.mod)
install-dev:
    @echo "Caching Go tool dependencies from tools.mod..."
    go mod download -modfile=tools.mod
    @echo "Done! Development tools are available through go tool -modfile=tools.mod."

# Remove build artifacts
clean:
    rm -rf bin/ coverage.out coverage.html
    go clean