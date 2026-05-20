#!/bin/sh

set -eu

if [ "$#" -lt 1 ]; then
    echo "usage: go-build.sh <source-root> [go-build-args...]" >&2
    exit 2
fi

src_root="$1"
shift

cd "$src_root"
exec go build "$@"
