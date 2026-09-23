#!/usr/bin/env bash
set -euo pipefail

# Custom fixture build. Provision does not infer a framework build command.
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
target_arch="${1:-amd64}"
case "$target_arch" in
  amd64|arm64) ;;
  *) echo "usage: $0 [amd64|arm64]" >&2; exit 2 ;;
esac
output_dir="$repo_dir/examples/host-http/dist"
mkdir -p "$output_dir"
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
(cd "$repo_dir" && GOOS=linux GOARCH="$target_arch" CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-buildid=' -o "$build_dir/hello" ./examples/host-http/hello)
chmod 0755 "$build_dir/hello"
archive="$output_dir/hello-linux-$target_arch.tar.gz"
(cd "$repo_dir" && go run ./examples/host-http/package --input "$build_dir/hello" --output "$archive")
printf 'Artifact: %s\n' "$archive"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$archive"
else
  shasum -a 256 "$archive"
fi
