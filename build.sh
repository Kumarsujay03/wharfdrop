#!/bin/sh
# Build release binaries for Linux, Windows and macOS into dist/.
# Usage: ./build.sh [version]     (default: version from `git describe`)
set -e
cd "$(dirname "$0")"
[ -x "$HOME/.local/go/bin/go" ] && export PATH="$HOME/.local/go/bin:$PATH"
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X github.com/Kumarsujay03/wharfdrop/internal/daemon.Version=$VERSION"
rm -rf dist && mkdir dist
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}; arch=${target#*/}
  ext=""; [ "$os" = windows ] && ext=".exe"
  out="dist/wharfdrop-$VERSION-$os-$arch$ext"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="$LDFLAGS" -o "$out" ./cmd/wharfdrop
  echo "built $out"
done
(cd dist && sha256sum wharfdrop-* > SHA256SUMS)
