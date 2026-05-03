#!/bin/sh
# Wrapper that runs `go build` from the project source root.
# Meson invokes custom_target commands from the build directory; this script
# changes into the source root so go build sees go.mod and the package layout.

set -eu

if [ "$#" -lt 1 ]; then
    echo "usage: go-build.sh <source-root> [go-build-args...]" >&2
    exit 2
fi

src_root="$1"
shift

cd "$src_root"
exec go build "$@"
