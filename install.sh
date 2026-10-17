#!/bin/sh
# Wharfdrop installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/Kumarsujay03/wharfdrop/main/install.sh | sh
#
# Installs the latest release to ~/.local/bin/wharfdrop after checking its
# SHA-256 against the release's SHA256SUMS. Does not start anything at login.
set -eu
repo=Kumarsujay03/wharfdrop
bindir="${WHARFDROP_BIN_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "Unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac

tag=$(curl -fsSI "https://github.com/$repo/releases/latest" | tr -d '\r' | sed -n 's#^[Ll]ocation: .*/releases/tag/\(v[0-9.]*\)$#\1#p')
[ -n "$tag" ] || { echo "Could not find the latest release" >&2; exit 1; }
asset="wharfdrop-$tag-$os-$arch"
base="https://github.com/$repo/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset ..."
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
want=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
if command -v sha256sum >/dev/null; then got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1); fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "Checksum mismatch, not installing" >&2; exit 1; }

mkdir -p "$bindir"
[ -x "$bindir/wharfdrop" ] && "$bindir/wharfdrop" stop --force >/dev/null 2>&1 || true
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$bindir/wharfdrop"
echo "Wharfdrop $tag installed to $bindir/wharfdrop"
case ":$PATH:" in *":$bindir:"*) ;; *) echo "Add $bindir to your PATH, e.g.: echo 'export PATH=\"$bindir:\$PATH\"' >> ~/.profile" ;; esac
echo "Start it with: wharfdrop ui      Stop it with: wharfdrop stop"
