#!/bin/sh

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

# Excludelist follows AppImage upstream: host-coupled libs (loader, GL, X11/Wayland, kernel) break when bundled.
lddtree -l "$APPDIR/$BIN_REL" | while read -r path; do
	name=$(basename "$path")
	case "$name" in
		ld-musl-*|libc.musl-*|ld-linux-*|terrain) continue ;;
		libGL.so.*|libEGL.so.*|libGLX.so.*|libGLdispatch.so.*) continue ;;
		libOpenGL.so.*|libgbm.so.*|libvulkan.so.*) continue ;;
		libdrm.so.*|libdrm_*.so.*) continue ;;
		libLLVM*.so.*|libSPIRV-Tools.so*) continue ;;
		libX11.so.*|libX11-xcb.so.*|libXau.so.*|libXdmcp.so.*) continue ;;
		libXcursor.so.*|libXdamage.so.*|libXext.so.*|libXfixes.so.*) continue ;;
		libXi.so.*|libXinerama.so.*|libXrandr.so.*) continue ;;
		libXrender.so.*|libXxf86vm.so.*) continue ;;
		libxcb.so.*|libxcb-*.so.*) continue ;;
		libwayland-client.so.*|libwayland-cursor.so.*) continue ;;
		libwayland-egl.so.*|libwayland-server.so.*) continue ;;
		libudev.so.*|libsystemd.so.*) continue ;;
		libgcc_s.so.*|libstdc++.so.*) continue ;;
		libz.so.*|libexpat.so.*) continue ;;
		libfreetype.so.*|libfontconfig.so.*) continue ;;
		libharfbuzz.so.*|libgmp.so.*) continue ;;
	esac
	[ -f "$path" ] || continue

	# Install under SONAME so dynamic linker NEEDED lookups resolve against real-file paths.
	soname=$(readelf -d "$path" 2>/dev/null \
		| sed -n 's/.*(SONAME).*\[\(.*\)\].*/\1/p' | head -1)
	[ -n "$soname" ] || soname=$name

	dest="$APPDIR/usr/lib/$soname"
	[ -e "$dest" ] && continue
	install -m644 "$path" "$dest"
done

# DESTDIR install skips this; without it GTK can't read Adwaita's gschema and segfaults.
glib-compile-schemas "$APPDIR/usr/share/glib-2.0/schemas"

cat > "$APPDIR/AppRun" <<'EOF'
#!/bin/sh
HERE=$(dirname "$(readlink -f "$0")")
export LD_LIBRARY_PATH="$HERE/usr/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export XDG_DATA_DIRS="$HERE/usr/share:${XDG_DATA_DIRS:-/usr/local/share:/usr/share}"
exec "$HERE/usr/bin/terrain" "$@"
EOF
chmod +x "$APPDIR/AppRun"

ln -sf "usr/share/applications/$APP_ID.desktop" "$APPDIR/$APP_ID.desktop"
ln -sf "usr/share/icons/hicolor/scalable/apps/$APP_ID.svg" "$APPDIR/$APP_ID.svg"
ln -sf "usr/share/icons/hicolor/scalable/apps/$APP_ID.svg" "$APPDIR/.DirIcon"

# --no-appstream: meson's data/ already runs appstreamcli validate.
ARCH=$(uname -m) appimagetool --no-appstream "$APPDIR" "$OUTPUT"
echo "$OUTPUT"
