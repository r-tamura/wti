# wti task runner. Run `just` to list recipes.

# Where `just install` puts the binary (override: `just bindir=/usr/local/bin install`).
bindir := env_var('HOME') / ".local/bin"

# Version metadata derived from git tags; injected via -ldflags.
version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
commit  := `git rev-parse --short HEAD 2>/dev/null || echo none`
date    := `date +%Y-%m-%d`
ldflags := "-s -w" + \
    " -X main.version=" + version + \
    " -X main.commit=" + commit + \
    " -X main.date=" + date

# List available recipes.
default:
    @just --list

# Build the binary into ./wti (git-ignored).
build:
    go build -ldflags "{{ldflags}}" -o wti .

# Build and install into {{bindir}}.
install:
    go build -ldflags "{{ldflags}}" -o "{{bindir}}/wti" .
    @echo "installed {{version}} -> {{bindir}}/wti"

# Run the full test suite with the race detector.
test:
    go test -race ./...

# go vet.
vet:
    go vet ./...

# golangci-lint (uses .golangci.yml).
lint:
    golangci-lint run ./...

# Format the code.
fmt:
    gofmt -w .

# Tidy go.mod / go.sum.
tidy:
    go mod tidy

# Everything CI runs: vet + test + lint.
ci: vet test lint

# Show the version string that would be embedded.
version:
    @echo "{{version}} (commit {{commit}}, built {{date}})"

# Dry-run a release build locally (requires goreleaser).
release-snapshot:
    goreleaser release --snapshot --clean
