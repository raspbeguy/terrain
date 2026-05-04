project('terrain',
  version: '0.1.0',
  meson_version: '>= 1.0.0',
  license: 'GPL-3.0-or-later',
)

# Toolchain
go_bin = find_program('go', required: true)
go_version_check = run_command(go_bin, 'version', check: true).stdout().strip()
message('Using Go: ' + go_version_check)

blueprint_compiler = find_program('blueprint-compiler', required: true, version: '>= 0.10')
glib_compile_resources = find_program('glib-compile-resources', required: true)

# Runtime dependencies (cgo links against these via gotk4)
dependency('gtk4', version: '>= 4.10')
dependency('libadwaita-1', version: '>= 1.4')

app_id = 'io.github.raspbeguy.Terrain'
app_name = 'Terrain'
app_summary = 'GNOME GUI for Terraform and OpenTofu'

conf = configuration_data()
conf.set('APP_ID', app_id)
conf.set('APP_NAME', app_name)
conf.set('APP_SUMMARY', app_summary)
conf.set('VERSION', meson.project_version())

subdir('data')

# Compile Blueprint files (.blp) to GtkBuilder XML (.ui)
blueprint_sources = [
  'window',
  'workspace-detail',
  'preferences',
  'run-detail',
  'add-remote',
  'var-edit',
  'varsets',
  'state-diff',
  'workspace-settings',
]

blueprint_targets = []
foreach name : blueprint_sources
  blueprint_targets += custom_target(name + '-ui',
    input: files('data/ui/blueprints/' + name + '.blp'),
    output: name + '.ui',
    command: [blueprint_compiler, 'compile', '--output', '@OUTPUT@', '@INPUT@'],
  )
endforeach

# Bundle .ui files into a binary gresource. --sourcedir points at the build
# directory because that's where the .ui files end up after blueprint compile.
gresource_xml = files('data/io.github.raspbeguy.Terrain.gresource.xml')

terrain_gresource = custom_target('terrain-gresource',
  input: gresource_xml,
  output: 'terrain.gresource',
  command: [
    glib_compile_resources, '@INPUT@',
    '--target=@OUTPUT@',
    '--sourcedir=' + meson.current_build_dir(),
  ],
  depends: blueprint_targets,
)

# Copy the gresource bundle into the source tree so `go build -tags
# embed_gresource` can find it via go:embed. This file is .gitignored.
embedded_gresource = custom_target('embed-gresource',
  input: terrain_gresource,
  output: 'gresource.stamp',
  command: [
    'sh', '-c',
    'cp "$1" "$2" && touch "$3"',
    'sh',
    '@INPUT@',
    meson.project_source_root() / 'internal' / 'resources' / 'terrain.gresource',
    '@OUTPUT@',
  ],
)

go_build_sh = find_program('build-aux/meson/go-build.sh', required: true)

terrain_bin = custom_target('terrain-binary',
  output: 'terrain',
  command: [
    go_build_sh,
    meson.project_source_root(),
    '-tags', 'embed_gresource',
    '-o', meson.current_build_dir() / 'terrain',
    '-ldflags', '-X main.version=' + meson.project_version(),
    './cmd/terrain',
  ],
  build_by_default: true,
  build_always_stale: true,
  depends: [embedded_gresource],
  install: true,
  install_dir: get_option('bindir'),
  console: true,
)

# Post-install: refresh icon cache + compile gschemas
gnome = import('gnome')
gnome.post_install(
  glib_compile_schemas: true,
  gtk_update_icon_cache: true,
  update_desktop_database: true,
)

# Boot smoke test — runs the binary under xvfb-run and grep the log for
# panics / Gtk-CRITICAL. Skipped automatically when xvfb-run isn't
# installed (CI without an X stack, etc.). The test executes against the
# fresh `terrain_bin` artifact, so it runs after every rebuild that
# touches UI.
xvfb_run = find_program('xvfb-run', required: false)
if xvfb_run.found()
  smoke_sh = find_program('build-aux/meson/smoke.sh', required: true)
  test('boot-smoke',
    smoke_sh,
    args: [terrain_bin, '4'],
    timeout: 30,
    is_parallel: false,
  )
endif

