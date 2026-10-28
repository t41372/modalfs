#!/bin/sh
# Build the download agent's Go tools with the same patched Go as its proxy.
# Upstream release binaries can still contain an old Go runtime. Callers pass
# GO_VERSION from app.py; the local test tools and the Modal image use this script.
set -eu

export GOTOOLCHAIN="go${GO_VERSION:?set GO_VERSION to the version in app.py}"
export CGO_ENABLED=0
mkdir -p "${1:?pass the output directory}"
output=$(cd "$1" && pwd)
build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM

fetch_source() (
    name=$1; repo=$2; version=$3; checksum=$4
    archive="$build_dir/$name.tgz"
    curl -fsSL -o "$archive" "https://codeload.github.com/$repo/tar.gz/refs/tags/$version"
    python3 - "$archive" "$checksum" <<'PY'
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
if hashlib.sha256(path.read_bytes()).hexdigest() != sys.argv[2]:
    sys.exit(f"checksum mismatch: {path}")
PY
    mkdir "$build_dir/$name"
    tar -xzf "$archive" -C "$build_dir/$name" --strip-components=1
)

fetch_source gopeed GopeedLab/gopeed v2.0.0-beta.3 ff12333a104723e2da4264133b4b25f27fb0bd4e60a30cdcf719e670d011d4a9
(
    cd "$build_dir/gopeed"
    # modalfs uses only the REST API. Satisfy the web target's embed without
    # building the unused Flutter UI; its flags also configure the API server.
    mkdir -p cmd/web/dist
    touch cmd/web/dist/index.html
    # The release's go.mod predates these fixes. Pin the updates so rebuilding
    # the same agent doesn't pick up arbitrary new dependency versions.
    go get github.com/go-git/go-git/v5@v5.19.3 \
        github.com/imroc/req/v3@v3.61.0 \
        github.com/quic-go/quic-go@v0.64.1 \
        github.com/refraction-networking/utls@v1.8.2 \
        github.com/pion/dtls/v3@v3.1.15 \
        github.com/pion/stun/v3@v3.1.7 \
        github.com/klauspost/compress@v1.20.3 \
        golang.org/x/crypto@v0.58.0 \
        golang.org/x/net@v0.62.0 \
        golang.org/x/text@v0.43.0 \
        go.opentelemetry.io/otel@v1.47.0
    go build -mod=readonly -tags web -trimpath -ldflags='-s -w' -o "$output/gopeed" ./cmd/web
)

fetch_source wgcf ViRb3/wgcf v2.3.1 993b39e3085c9b53556af0e8297859c36ef1a5aeebf51378dabde49479ec1b7d
(
    cd "$build_dir/wgcf"
    go get github.com/andybalholm/brotli@v1.2.7 github.com/klauspost/compress@v1.20.3
    go build -mod=readonly -trimpath -ldflags='-s -w' -o "$output/wgcf" .
)
