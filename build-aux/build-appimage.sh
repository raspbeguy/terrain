#!/bin/sh
# Assemble a self-contained AppImage from an AppDir staged by meson install.
#
# Expected setup before invocation:
#   meson setup build --prefix=/usr
#   meson compile -C build
#   DESTDIR="$PWD/AppDir" meson install -C build
#
# The script then bundles every shared library `terrain` needs into
# AppDir/usr/lib, compiles gschemas that DESTDIR-skipped post-install
# hooks would have, writes an AppRun launcher that points GTK at the
# bundled libs and data, and hands the AppDir to appimagetool.
#
# Required commands (Alpine: `apk add appimagetool pax-utils
# squashfs-tools glib-dev`):
#   appimagetool lddtree glib-compile-schemas

set -eu

APPDIR=${APPDIR:-AppDir}
OUTPUT=${OUTPUT:-terrain-$(uname -m).AppImage}
APP_ID=io.github.raspbeguy.Terrain
BIN_REL=usr/bin/terrain

if [ ! -x "$APPDIR/$BIN_REL" ]; then
	echo "$0: $APPDIR/$BIN_REL missing; run 'meson install' first" >&2
	exit 1
fi

mkdir -p "$APPDIR/usr/lib"

# The musl loader and libc *are* the ABI we target. Bundling them would
# make the AppImage refuse to run on glibc hosts AND break on Alpine
# version mismatches. Same for any rare glibc loader name that sneaks
# in. Everything else, we own.
lddtree -l "$APPDIR/$BIN_REL" | while read -r path; do
	name=$(basename "$path")
	case "$name" in
		ld-musl-*|libc.musl-*|ld-linux-*|terrain) continue ;;
	esac
	[ -f "$path" ] || continue

	# Install under the soname so the dynamic linker's NEEDED lookup
	# resolves; symlink-named source paths already match, but resolved
	# real-file paths (e.g. .so.1.0.0) do not.
	soname=$(readelf -d "$path" 2>/dev/null \
		| sed -n 's/.*(SONAME).*\[\(.*\)\].*/\1/p' | head -1)
	[ -n "$soname" ] || soname=$name

	dest="$APPDIR/usr/lib/$soname"
	[ -e "$dest" ] && continue
	install -m644 "$path" "$dest"
done

# DESTDIR-installs skip these; without them GTK fails to read the theme
# (Adwaita CSS lookup goes through org.gnome.desktop.interface gschema)
# and segfaults later, and the app's own gschema can't be read.
glib-compile-schemas "$APPDIR/usr/share/glib-2.0/schemas"

# AppRun rewrites the lib and XDG search paths to point inside the
# mounted AppImage. We avoid patching rpaths because that grows shared
# libs by a half-meg each and has historically broken constructor-driven
# GResource registration on some builds.
cat > "$APPDIR/AppRun" <<'EOF'
#!/bin/sh
HERE=$(dirname "$(readlink -f "$0")")
export LD_LIBRARY_PATH="$HERE/usr/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export XDG_DATA_DIRS="$HERE/usr/share:${XDG_DATA_DIRS:-/usr/local/share:/usr/share}"
exec "$HERE/usr/bin/terrain" "$@"
EOF
chmod +x "$APPDIR/AppRun"

# appimagetool reads these from the AppDir root.
ln -sf "usr/share/applications/$APP_ID.desktop" "$APPDIR/$APP_ID.desktop"
ln -sf "usr/share/icons/hicolor/scalable/apps/$APP_ID.svg" "$APPDIR/$APP_ID.svg"
ln -sf "usr/share/icons/hicolor/scalable/apps/$APP_ID.svg" "$APPDIR/.DirIcon"

# --no-appstream skips a stricter-than-meson validator; data/meson.build
# already runs appstreamcli validate when it's installed.
ARCH=$(uname -m) appimagetool --no-appstream "$APPDIR" "$OUTPUT"
echo "$OUTPUT"
