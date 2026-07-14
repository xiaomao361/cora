#!/usr/bin/env bash
set -euo pipefail

version=${1:?usage: build-release.sh VERSION [OUTPUT_DIR]}
output_dir=${2:-dist/$version}
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"

if [[ ! "$version" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "version may contain only letters, numbers, dot, underscore, and dash" >&2
  exit 1
fi

if test -n "$(git status --porcelain)"; then
  echo "refusing release build from a dirty working tree" >&2
  exit 1
fi

commit=$(git rev-parse HEAD)
build_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ldflags="-s -w -X github.com/claracore/cora/internal/buildinfo.Version=$version -X github.com/claracore/cora/internal/buildinfo.Commit=$commit -X github.com/claracore/cora/internal/buildinfo.BuildTime=$build_time"

mkdir -p "$output_dir"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$output_dir/cora-server" ./cmd/cora-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$output_dir/cora-agent" ./cmd/cora-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$output_dir/cora-canary" ./cmd/cora-canary

(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum cora-* > SHA256SUMS
  else
    shasum -a 256 cora-* > SHA256SUMS
  fi
)

echo "built Cora $version from $commit in $output_dir"
