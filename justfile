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

# end-to-end tests: real mounts (NFS on macOS, FUSE on Linux) against a fake Modal with a slow network; no account needed
test-e2e:
    go test -tags e2e -count=1 -timeout 40m ./internal/e2e

# the end-to-end tests against real Modal: each test CREATES AND DELETES a scratch volume in the profile's workspace
test-e2e-live profile *args:
    MODALFS_E2E_PROFILE={{profile}} go test -tags e2e -count=1 -timeout 60m ./internal/e2e {{args}}

# Modal behaviour modalfs relies on that Modal doesn't document, checked against real Modal: each test CREATES AND DELETES a scratch volume in the profile's workspace
test-modal-contract profile *args:
    MODALFS_E2E_PROFILE={{profile}} go test -tags live -count=1 -timeout 15m -v -run Contract ./internal/modalapi {{args}}

# the remote download agent's tests: the agent's own (Python) and the Go side driving it, outside Modal
test-agent:
    #!/bin/sh
    set -eu
    tools="${XDG_CACHE_HOME:-$HOME/.cache}/modalfs/agent-tools"
    if [ -z "${GOPEED_BIN:-}" ] && [ -x "$tools/gopeed" ]; then export GOPEED_BIN="$tools/gopeed"; fi
    (cd internal/downloads/remote && python3 -m unittest -v test_app)
    go test -race -count=1 -run TestLocalRunner -v ./internal/downloads
    go test -race -count=1 -run TestDownloadWithTheLocalAgent -v ./internal/daemon

# fetch the gopeed build the agent tests use, for this machine (they skip without it)
agent-tools:
    #!/bin/sh
    set -eu
    tools="${XDG_CACHE_HOME:-$HOME/.cache}/modalfs/agent-tools"
    version=$(sed -n 's/^GOPEED_VERSION = "\(.*\)"/\1/p' internal/downloads/remote/app.py)
    case "$(uname -s)" in Darwin) os=macos ;; *) os=linux ;; esac
    case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; *) arch=amd64 ;; esac
    tmp=$(mktemp -d)
    curl -fsSL -o "$tmp/g.zip" "https://github.com/GopeedLab/gopeed/releases/download/$version/gopeed-web-$version-$os-$arch.zip"
    unzip -q "$tmp/g.zip" -d "$tmp"
    mkdir -p "$tools"
    mv "$tmp"/gopeed-web-*/gopeed "$tools/gopeed"
    rm -rf "$tmp"
    if [ "$os" = macos ]; then xattr -d com.apple.quarantine "$tools/gopeed" 2>/dev/null || true; fi
    echo "$tools/gopeed ($version)"

# remote downloads against real Modal: CREATES AND DELETES a scratch volume and a test app in the profile's workspace (costs cents)
test-downloads-live profile *args:
    MODALFS_E2E_PROFILE={{profile}} go test -tags live -count=1 -timeout 60m -v ./internal/downloads {{args}}

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
