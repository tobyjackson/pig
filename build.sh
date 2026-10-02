#!/bin/sh
# build.sh builds pig with the version stamped in from the git tag, so a local
# build and a released binary report the same thing. The release workflow uses
# the same ldflags, so there is one source of truth: `git describe`.
#
#   ./build.sh              # build ./pig
#   ./build.sh -o /tmp/pig  # extra args go to go build
set -eu

cd "$(dirname "$0")"

version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
out=./pig
[ "${1:-}" = "-o" ] && out=$2

exec go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "$out" ./cmd/pig
