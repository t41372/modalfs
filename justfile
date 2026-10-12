# Run `just` to list recipes.

ldflags := "-s -w"
# packages that build without cgo (everything except the desktop app)
pkgs := "./internal/... ./cmd/modalfs"

[private]
default:
    @just --list

# install the UI's dependencies (needs Bun)
[private]
web-deps:
    cd web && bun install --frozen-lockfile

# build the UI into internal/webui/dist, where the Go code embeds it
ui: web-deps
    cd web && bun run build

# run the UI with hot reload on fake data (no daemon, no Modal account needed)
dev-mock: web-deps
    cd web && VITE_MOCK=1 bun run dev

# run the UI with hot reload against your running daemon (start it with `modalfs daemon`)
dev: web-deps
    cd web && MODALFS_UI="$(go run ../cmd/modalfs ui --print)" bun run dev

# build the CLI + daemon for this machine → dist/modalfs
build: ui
    go build -trimpath -ldflags "{{ldflags}}" -o dist/modalfs ./cmd/modalfs

# cross-compile the CLI + daemon for macOS and Linux (pure Go, no cgo)
cli: ui
    #!/bin/sh
    set -eu
    for t in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
      os=${t%/*}; arch=${t#*/}
      echo "dist/modalfs-$os-$arch"
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "{{ldflags}}" -o dist/modalfs-$os-$arch ./cmd/modalfs
    done

# build dist/modalfs.app, universal arm64 + x86_64 (run on a Mac)
app: ui
    ./packaging/build-macos-app.sh

# build the Linux desktop app (needs libgtk-3-dev and libwebkit2gtk-4.1-dev)
linux-gui: ui
    CGO_ENABLED=1 go build -trimpath -tags desktop,production,webkit2_41 -ldflags "{{ldflags}}" -o dist/modalfs-gui ./cmd/modalfs-gui

# format all Go code
fmt:
    gofmt -w cmd internal

# check formatting, go vet, and typecheck the UI
lint: web-deps
    #!/bin/sh
    set -eu
    unformatted=$(gofmt -l cmd internal)
    if [ -n "$unformatted" ]; then echo "needs gofmt (run 'just fmt'):"; echo "$unformatted"; exit 1; fi
    go vet {{pkgs}}
    go vet -tags integration {{pkgs}}
    cd web && bun run typecheck

# unit tests with the race detector (offline: no Modal account needed)
test:
    go test -race {{pkgs}}

# end-to-end tests: real NFS mounts (macOS) against a fake Modal with a slow network; no account needed
test-e2e:
    go test -tags e2e -count=1 -timeout 25m ./internal/e2e

# run a fake Modal for trying modalfs without an account (prints the variables to set)
fake *args:
    go run ./cmd/modalfs-fake {{args}}

# integration tests: CREATE AND DELETE files in the volume named by MODALFS_TEST_VOLUME
test-integration:
    @test -n "${MODALFS_TEST_VOLUME:-}" || { echo "set MODALFS_TEST_VOLUME to a scratch volume"; exit 1; }
    go test -tags integration {{pkgs}}

# remove build output
clean:
    rm -rf dist
    rm -rf internal/webui/dist && mkdir internal/webui/dist && touch internal/webui/dist/.gitkeep
