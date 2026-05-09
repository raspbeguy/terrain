#!/bin/sh
# smoke.sh: boot the terrain binary under xvfb-run and verify it reaches
# the "window built" log line without panicking or emitting Gtk-CRITICAL
# warnings. Run as a meson test after every rebuild that touches UI code.
#
# Usage: smoke.sh <binary-path> [duration-seconds]
#
# Exit codes:
#   0   clean boot, "window built" reached
#   1   panic, GTK critical, or did not reach "window built"
#   77  skipped (xvfb-run not installed; meson treats this as SKIP)

set -eu

if [ "$#" -lt 1 ]; then
    echo "usage: smoke.sh <binary-path> [duration-seconds]" >&2
    exit 2
fi

BINARY="$1"
DURATION="${2:-3}"

# Resolve the binary path to absolute. Meson passes custom_target outputs as
# bare filenames relative to the build directory; `timeout(1)` requires the
# command to be findable on PATH or absolute.
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

# Use a throwaway HOME so the test sees a fresh first-run state regardless
# of what's in the user's real $XDG_CONFIG_HOME. Matters for CI; on a dev
# machine this also keeps the test from creating runs in your real cache.
TESTHOME="$(mktemp -d)"
trap 'rm -rf "$TESTHOME"' EXIT

LOG="$TESTHOME/log"

# We expect the binary to be killed by `timeout` after $DURATION seconds.
# Capture stdout+stderr and don't fail the script on the timeout's non-zero
# exit: that's the success signal here.
HOME="$TESTHOME" XDG_CONFIG_HOME="$TESTHOME/.config" XDG_CACHE_HOME="$TESTHOME/.cache" \
    xvfb-run -a timeout "$DURATION" "$BINARY" --debug >"$LOG" 2>&1 || true

# Diagnostics first if something goes wrong.
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

# Bonus: surface any Gtk-WARNING that isn't environmental noise (libEGL,
# DRI3, dbus-launch). These don't fail the test but are worth printing so
# the user notices them.
if grep -E 'Gtk-WARNING|GLib-GObject-CRITICAL' "$LOG" \
    | grep -vE 'libEGL|DRI3|dbus-launch|atspi|session bus' >/dev/null 2>&1; then
    echo "smoke: NOTE: non-environmental Gtk warnings observed:" >&2
    grep -E 'Gtk-WARNING|GLib-GObject-CRITICAL' "$LOG" \
        | grep -vE 'libEGL|DRI3|dbus-launch|atspi|session bus' \
        | sed 's/^/  /' >&2
fi

echo "smoke: ok, window built cleanly"
