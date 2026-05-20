#!/bin/sh

set -eu

if [ "$#" -lt 1 ]; then
    echo "usage: smoke.sh <binary-path> [duration-seconds]" >&2
    exit 2
fi

BINARY="$1"
DURATION="${2:-3}"

# timeout(1) needs an absolute path; meson hands us a build-relative filename.
case "$BINARY" in
    /*) ;;
    *)  BINARY="$(pwd)/$BINARY" ;;
esac
if [ ! -x "$BINARY" ]; then
    echo "smoke: FAIL: binary not found or not executable: $BINARY" >&2
    exit 1
fi

if ! command -v xvfb-run >/dev/null 2>&1; then
    echo "smoke: xvfb-run not installed; skipping"
    exit 77
fi
if ! command -v timeout >/dev/null 2>&1; then
    echo "smoke: timeout(1) not installed; skipping"
    exit 77
fi

TESTHOME="$(mktemp -d)"
trap 'rm -rf "$TESTHOME"' EXIT

LOG="$TESTHOME/log"

# `timeout` killing the binary is the success signal here, so its non-zero exit is expected.
HOME="$TESTHOME" XDG_CONFIG_HOME="$TESTHOME/.config" XDG_CACHE_HOME="$TESTHOME/.cache" \
    xvfb-run -a timeout "$DURATION" "$BINARY" --debug >"$LOG" 2>&1 || true

if grep -qE 'panic:|Gtk-CRITICAL|nil pointer dereference|fatal error:|SIGSEGV' "$LOG"; then
    echo "smoke: FAIL: critical error in log:" >&2
    sed 's/^/  /' "$LOG" >&2
    exit 1
fi

if ! grep -q 'window built' "$LOG"; then
    echo "smoke: FAIL: did not reach 'window built' within ${DURATION}s" >&2
    sed 's/^/  /' "$LOG" >&2
    exit 1
fi

if grep -E 'Gtk-WARNING|GLib-GObject-CRITICAL' "$LOG" \
    | grep -vE 'libEGL|DRI3|dbus-launch|atspi|session bus' >/dev/null 2>&1; then
    echo "smoke: NOTE: non-environmental Gtk warnings observed:" >&2
    grep -E 'Gtk-WARNING|GLib-GObject-CRITICAL' "$LOG" \
        | grep -vE 'libEGL|DRI3|dbus-launch|atspi|session bus' \
        | sed 's/^/  /' >&2
fi

echo "smoke: ok, window built cleanly"
