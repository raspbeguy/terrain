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
# in. Beyond that we follow the AppImage upstream excludelist
# (https://github.com/AppImage/pkg2appimage/blob/master/excludelist):
# libraries that are tied to the host kernel/display/graphics stack
# fail in obscure ways when bundled, and the symbol-versioned ones
# (libgcc_s, libstdc++) need to match the host stdlib.
lddtree -l "$APPDIR/$BIN_REL" | while read -r path; do
	name=$(basename "$path")
	case "$name" in
		ld-musl-*|libc.musl-*|ld-linux-*|terrain) continue ;;
		# GPU / GL / Vulkan stack: kernel DRM ABI + driver layout.
		libGL.so.*|libEGL.so.*|libGLX.so.*|libGLdispatch.so.*) continue ;;
		libOpenGL.so.*|libgbm.so.*|libvulkan.so.*) continue ;;
		libdrm.so.*|libdrm_*.so.*) continue ;;
		# Mesa transitive bulk; not portable across host kernels.
		libLLVM*.so.*|libSPIRV-Tools.so*) continue ;;
		# X11 / Wayland client protocol libs talk to the host display
		# server; mismatching them severs the connection.
		libX11.so.*|libX11-xcb.so.*|libXau.so.*|libXdmcp.so.*) continue ;;
		libXcursor.so.*|libXdamage.so.*|libXext.so.*|libXfixes.so.*) continue ;;
		libXi.so.*|libXinerama.so.*|libXrandr.so.*) continue ;;
		libXrender.so.*|libXxf86vm.so.*) continue ;;
		libxcb.so.*|libxcb-*.so.*) continue ;;
		libwayland-client.so.*|libwayland-cursor.so.*) continue ;;
		libwayland-egl.so.*|libwayland-server.so.*) continue ;;
		# Kernel-ABI / host-services integration.
		libudev.so.*|libsystemd.so.*) continue ;;
		# Symbol versioning forces matching host stdlib.
		libgcc_s.so.*|libstdc++.so.*) continue ;;
		# Ubiquitous and ABI-stable on every musl distro; bundling them
		# just adds weight and risks GLIBCXX-style mismatches.
		libz.so.*|libexpat.so.*) continue ;;
		libfreetype.so.*|libfontconfig.so.*) continue ;;
		libharfbuzz.so.*|libgmp.so.*) continue ;;
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
