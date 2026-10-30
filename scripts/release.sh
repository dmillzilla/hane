#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
source_version=$(sed -n 's/^var Version = "\(.*\)"/\1/p' internal/buildinfo/version.go)
release_version=${1:-$source_version}
release_version=${release_version#v}
if [[ ! $release_version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || [[ $release_version != "$source_version" ]]; then
  echo "Release version must match internal/buildinfo/version.go ($source_version)." >&2
  exit 1
fi

mkdir -p dist
release_tmp=$(mktemp -d)
trap 'rm -rf "$release_tmp"' EXIT
archives=()
for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  target_os=${platform%/*}
  target_arch=${platform#*/}
  target_dir="$release_tmp/${target_os}_${target_arch}"
  mkdir -p "$target_dir"
  echo "Building Hane $release_version for $platform"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath \
    -ldflags "-s -w -X github.com/dmillzilla/hane/internal/buildinfo.Version=$release_version" \
    -o "$target_dir/hane" ./cmd/hane
  archive="hane_${release_version}_${target_os}_${target_arch}.tar.gz"
  tar -czf "dist/$archive" -C "$target_dir" hane
  archives+=("$archive")
done

cd dist
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "${archives[@]}" > checksums.txt
else
  shasum -a 256 "${archives[@]}" > checksums.txt
fi
