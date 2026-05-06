# Terrain — agent guide

A native GNOME desktop GUI for Terraform / OpenTofu, inspired by Terraform
Enterprise and OTF. Go + gotk4 + libadwaita; Blueprint UI compiled to a
gresource bundle; Flatpak distribution.

## Quickstart

```sh
# Build + run (host)
meson setup build              # only first time
meson compile -C build
./build/terrain                 # opens window
./build/terrain --diagnose      # non-GUI sanity: load config + backends, exit
./build/terrain --debug         # bump log level to debug
./build/terrain --version

# Tests
go test ./internal/...          # ~80 tests across domain / local / config / hcl / runner / widgets
go vet ./...
meson test -C build             # 3 tests: validate-desktop, validate-metainfo, boot-smoke

# Flatpak (built and tested before; ~1h fresh, much faster incrementally)
# --force-clean wipes the output staging dir (build-flatpak/) so flatpak-
# builder doesn't refuse to start a new build. It does NOT touch the cache
# (.flatpak-builder/), so ccache + downloaded sources + cgo intermediates
# are reused — incremental rebuilds are seconds-to-minutes after the first
# fresh build is done. Always pass --force-clean for repeated runs.
flatpak-builder --user --force-clean --ccache --repo=build-flatpak-repo \
    build-flatpak build-aux/flatpak/io.github.raspbeguy.Terrain.yml
flatpak build-bundle build-flatpak-repo terrain.flatpak io.github.raspbeguy.Terrain
```

The `meson test` boot-smoke runs the binary under `xvfb-run` and grep's the
log for panics / `Gtk-CRITICAL` / "window built". Catches builder-ID drift,
nil-widget crashes, and missing gresource entries automatically.

## Architecture rules (don't break)

1. **`internal/domain/`** has zero non-stdlib deps and never imports gotk4
   or any UI package. Pure types + interfaces; unit-testable headless.

2. **`internal/ui/bridge/`** is the canonical crossing point between domain
   channels and the GTK main thread. Every streaming domain → GTK
   transition (run events, log lines, plan results, done signals) goes
   through `bridge.PumpRun`. Domain code emits on `chan` exclusively; the
   bridge converts each item to an `IdleAdd`-scheduled callback that runs
   on the GTK main thread. Touching a GObject from a non-main goroutine is
   undefined behaviour (segfaults, not panics).

   The only sanctioned exception is `internal/ui/dialogs/addremote_idle.go`
   — a contained `glib.IdleAdd` wrapper used by the Add Remote dialog's
   one-shot Test Connection callback. New code should prefer `bridge`; only
   reach for the dialogs-local pattern when streaming through a `RunStream`
   would be obvious overkill for a single async result.

3. **The `Backend` interface** in `internal/domain/backend.go` is the
   architectural fulcrum. Local + remote both implement it; UI never
   branches on backend kind. Optional capabilities (`stateLoader`,
   `stateVersionLister`, `runListing`, `variableLoader`,
   `variableUpserter`) are type-asserted by the window — backends declare
   support by implementing the right method signature.

4. **The `Capabilities` bitmask** is how the UI knows what to show.
   Backends should not lie about supporting features; the UI gates entire
   sections on `Capabilities().Has(CapPolicy)` etc. OTF capabilities get
   refined at runtime by `remote.Backend.Probe()`.

5. **`-json` output everywhere**. Local runs always invoke `tofu plan -json`
   / `apply -json` / `show -json`. Human-readable mode is unparseable
   across versions; we re-render the structured stream into a coloured
   terminal-looking widget.

6. **Single tofu-invocation chokepoint**. Every `tofu` / `terraform` exec
   site routes through `internal/backend/local/sandbox.go:hostCommand`.
   It runs the binary directly when sandbox-accessible (managed binaries
   under `$XDG_DATA_HOME` or paths the Flatpak mounts) and falls back to
   `flatpak-spawn --host` only for host-only paths — the latter requires
   the `org.freedesktop.Flatpak` portal talk-name, which the Flathub
   manifest does NOT grant. Inside the Flathub bundle, only managed-
   binary subprocess mode works; container and bubblewrap modes need
   host access and degrade to a clear error. The runtime layer in
   `runtime.go` adds the two sandboxed paths: `containerRuntime`
   (podman/docker `run --rm --init …` against an OCI image) and
   `bubblewrapRuntime` (`bwrap` user-namespace sandbox around the host
   binary, no image system). Both delegate back to `hostCommand` so the
   Flatpak boundary stays at one crossing. **Do not** add new exec sites
   for tofu/terraform/podman/bwrap that bypass `hostCommand`.

7. **Binary resolution** is decoupled from the runtime via the
   `BinaryResolver` interface in `internal/backend/local/binary.go`.
   `pathResolver` discovers `tofu` / `terraform` on host PATH;
   `managedResolver` downloads + caches official releases under
   `$XDG_DATA_HOME/terrain/binaries/<engine>/<version>/` and verifies
   them against the upstream `_SHA256SUMS` file. The choice is per-
   workspace via `WorkspaceSettings.BinarySource` — `host` (zero value)
   keeps the existing behavior; `managed` pulls the version named by
   `WorkspaceSettings.ManagedEngine` + `ManagedVersion`. The shared
   resolver singleton (`sharedManagedResolver()`) means UI-triggered
   installs in Preferences and run-time resolutions in `runWorker` use
   the same per-(engine, version) install lock.

8. **Run mode lives per-workspace, not in `domain.Workspace.ExecutionMode`**.
   The TFE-mirror field is shared with the remote backend and has a fixed
   vocabulary; subprocess vs container is a local-backend implementation
   choice that lives in
   `$XDG_DATA_HOME/terrain/<backend>/<ws>/settings.json`
   (`local.LoadWorkspaceSettings` / `local.SaveWorkspaceSettings`).
   Each run also persists its effective mode + image into its
   `request.txt` snapshot so apply runs can reuse the producing plan's
   mode regardless of whether the workspace setting changed in between.
   State queries (`LoadState`, `snapshotState`) deliberately stay on
   the host binary even when a workspace is in container mode — they're
   short, synchronous, and forcing a container spin-up on every state-
   tab refresh would hurt UX. Documented limitation; revisit only if
   version-mismatch issues surface.

## Package layout

```
cmd/terrain/                main, --diagnose, --debug, --version flags
internal/
  domain/                   Backend interface, Workspace, Run, Variable,
                            VariableSet, StateVersion, RunStream, etc.
                            NO gotk4 imports.
  backend/local/            tofu/terraform CLI runner. exec.go (subprocess
                            line streaming + SIGINT cancel; Cancel hooks
                            chain so the runtime layer can install a pre-
                            cancel before SIGINT-to-wrapper), run.go (run
                            worker), runtime.go (Runtime interface +
                            hostRuntime + containerRuntime; path-translation
                            of -out=/-var-file=/positional plan paths is a
                            pure function `translateArgs`), wssettings.go
                            (per-workspace settings.json: run_mode +
                            image), variables.go (hcl + secrets), varsets.go
                            (per-set JSON manifest), snapshot.go (state
                            history + retention), materialize.go (TFE-style
                            var precedence), envindex.go (env-category index).
  backend/remote/           hashicorp/go-tfe wrapper. backend.go, run.go
                            (polling), variables.go, state.go, compat.go
                            (probing), state.go (state versions list/load).
  hcl/                      hashicorp/hcl/v2 helpers. variables.go (parse),
                            varwrite.go (hclwrite round-trip).
  config/                   $XDG_CONFIG_HOME/terrain/config.toml registry,
                            BackendConfig {local,remote}, BuildBackends.
  secrets/                  zalando/go-keyring wrapper (libsecret on Linux).
                            ResolveToken pattern: keyring first, plaintext
                            config fallback with warning.
  runner/                   history.go (ndjson run log per workspace),
                            locks.go (per-workspace mutex registry).
  resources/                gresource bundle embedded via go:embed; build
                            tag `embed_gresource` toggles between empty
                            (dev) and populated (meson-built).
  ui/
    app.go                  AdwApplication, gio.SimpleActions, accelerators.
    window/                 main window controller, sidebar grouping,
                            backend lookup, capability type-asserts.
    bridge/                 PumpRun: domain channels → GTK main thread.
    dialogs/                AddLocal, AddRemote, Preferences, EditVariable,
                            Varsets, StateDiff.
    views/run/              run detail (log + plan diff tabs).
    views/workspace/        workspace detail (overview + runs + vars + state).
    widgets/                LogView, PlanDiff, StateTree, StateDiff, VarList.
    uihelpers/              MustCast for GtkBuilder objects.
data/
  ui/blueprints/            *.blp source files (8 files: window, workspace-
                            detail, run-detail, preferences, add-remote,
                            var-edit, varsets, state-diff)
  io.github.raspbeguy.Terrain.{desktop.in, metainfo.xml.in, gschema.xml,
                                gresource.xml, svg}
build-aux/
  flatpak/                  Flatpak manifest. Targets GNOME 50 + golang
                            Sdk extension (any current branch); inherits
                            GOTOOLCHAIN=auto so go.mod's pinned version
                            gets fetched.
  meson/                    go-build.sh (custom_target wrapper, runs go
                            build from source root with absolute output
                            path) + smoke.sh (xvfb boot smoke test for
                            meson test).
testdata/                   Fixtures used by integration tests.
```

## Conventions

- **Comments**: default to none. Add one only when the WHY is non-obvious
  — a hidden invariant, a subtle constraint, a workaround for a specific
  bug. One short line max; no multi-paragraph docstrings, no field-by-
  field prose for self-explanatory names. Don't explain WHAT the code
  does (names already do that). Don't reference the current task / fix /
  commit date / "the user" ("verifies the 2026-05-03 change", "added for
  the X flow") — that rots fast and belongs in commit messages or the PR.

- **Blueprint vs Go**: static layouts (windows, dialogs, page skeletons)
  live in `.blp` files; dynamic content (list rows, dropdown contents,
  per-resource diff rows) is built in Go via `gtk.SignalListItemFactory`
  or direct widget construction. The MustCast helper at
  `internal/ui/uihelpers/builder.go` panics with a clear message on
  builder-ID drift — failures are loud at startup, not silent at runtime.

- **Error handling**: domain errors are sentinels (`domain.ErrNotFound`,
  `domain.ErrNotImplemented`); compare with `errors.Is`. Backend
  implementations wrap with `%w` to preserve the chain. UI shows toast
  errors via `Window.ToastError(msg)` for user-actionable failures and
  logs via `slog` for diagnostic-only ones.

- **slog levels**: `Info` for state transitions visible in the UI (run
  started, variable saved, backend connected). `Debug` for click handlers
  and per-event spam — gated by `--debug` or `TERRAIN_DEBUG=1`. `Warn` for
  recoverable failures the user might want to know about. `Error` for
  things the user definitely needs to see (almost always paired with a
  Toast).

- **File paths**:
  - `$XDG_CONFIG_HOME/terrain/config.toml` — backend registry (durable)
  - `$XDG_CONFIG_HOME/terrain/varsets/<id>.json` — variable set manifests
  - `$XDG_CACHE_HOME/terrain/<backend>/<ws>/runs/<id>/` — run artifacts
    (ephemeral; deleted by cache cleanup is fine)
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/state-versions/<id>/` — state
    snapshots (durable; retention: keep newest 50 + last 30 days)
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/overrides.tfvars` — terrain-
    managed plain (non-sensitive) variable values. Loaded with the
    project's own tfvars at run-materialize time and passed via
    `-var-file=` so it overrides the project's defaults. Living outside
    the project tree means terrain-managed values can never be
    accidentally committed to the user's repo.
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/env-vars.json` — env-category
    variable name index (names only; values in keyring). Same out-of-
    project rationale as overrides.tfvars.
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/settings.json` — per-workspace
    overrides for the runtime layer: `{run_mode: "subprocess"|"container",
    image: "..."}`. Zero value = inherit `AppConfig.DefaultRunMode` /
    `DefaultImageTofu` / `DefaultImageTerraform`. Edited via the gear
    button in the workspace overview header.
  - `$XDG_CACHE_HOME/terrain/<backend>/<ws>/plugins-container/` — provider
    plugin cache mounted into the container as `TF_PLUGIN_CACHE_DIR`.
    Kept separate from the host's `.terraform/` because container glibc /
    arch may not match the host's, so lock-file hashes diverge. Safe to
    wipe — tofu re-downloads on next init.

- **Secrets**: never plaintext on disk if avoidable. Sensitive variable
  values + remote backend tokens go to the system keyring (libsecret on
  Linux via go-keyring). Plaintext fallback in `config.toml` is allowed
  when keyring is unreachable, with a logged warning. Run-time materializer
  pulls sensitive values from the keyring into a per-run
  `vars.auto.tfvars` (0600 perms; HCL — preserves cty types so list/object
  variables don't collapse to strings and trip "Invalid value for input
  variable") that's `defer os.Remove`'d on terminal status.

## Common gotchas

- **Two `glib` packages exist**: `gotk4/pkg/core/glib` (internal) and
  `gotk4/pkg/glib/v2` (the GLib v2 binding). They are not interchangeable.
  `gio.SimpleAction.ConnectActivate` expects `func(*glib.Variant)` from
  `glib/v2`. Using the core flavour produces an opaque type-mismatch
  error.

- **Generic type-parameter unions can't include interfaces with methods**.
  `func cast[T gtk.Widgetter | *adw.Foo]` fails. Use `func cast[T any]`
  and `obj.Cast().(T)` inside.

- **AdwApplicationWindow → gtk.Window field path**: pass
  `&w.root.Window` (the embedded gtk.Window struct field) to functions
  that want `*gtk.Window`, not `&w.root` itself.

- **First gotk4 build is slow**: ~30–45 minutes on the dev VM (single-
  threaded in places, cgo against gtk4 headers). Subsequent builds are
  seconds. The Flatpak build always pays the full cost because the
  sandbox starts fresh each time.

- **`golang.org/x/text` / `golang.org/x/sync` upgrades invalidate the Go
  compile cache** for everything that transitively imports them — which
  includes gotk4. Adding dependencies that bump these is expensive.

- **GNOME 47 is EOL** (since Oct 2025); GNOME 49 is the previous-stable
  cycle. The Flatpak manifest targets GNOME 50 (released March 2026,
  current latest). The golang Sdk extension at branch 25.08+ (paired
  with fdo SDK 25.08) ships Go 1.25.x; `GOTOOLCHAIN=auto` lets `go`
  fetch the 1.26 toolchain pinned in go.mod when needed.

- **flatpak-builder needs `--share=network` during build** (to fetch Go
  modules). Acceptable for personal builds; for Flathub submission, vendor
  the Go modules first (`go mod vendor`, declare vendor dir as source,
  drop the network share).

## Where to read more

- `/home/alpine/.claude/plans/let-s-develop-a-gnome-melodic-lightning.md`
  — the original architectural plan, milestones M0–M5, every divergence
  documented in the plan-vs-actual audit.

- `/home/alpine/.claude/projects/-home-alpine-repo-terrain/memory/` —
  auto-saved memory files: project overview, build environment, gotk4
  gotchas, Flatpak runtime versions.

- `README.md` — user-facing description + install instructions.

## Don't (load-bearing)

- Don't add new `glib.IdleAdd` call sites outside `internal/ui/bridge/`
  unless you have the same justification as `dialogs/addremote_idle.go`
  (one-shot async result that doesn't fit the stream-of-events shape).
  The default is bridge — divergences need a comment explaining why.
- Don't import `internal/ui/...` from `internal/domain/...` or
  `internal/backend/...`. The dependency arrow goes one way: domain ← ui.
- Don't add fields to `domain.Backend` interface methods that only one
  backend supports. Add an optional capability interface in
  `internal/ui/window/window.go` and type-assert there. Examples already
  exist: `stateLoader`, `stateVersionLister`, `runListing`,
  `variableLoader`, `variableUpserter`.
- Don't write secrets to `config.toml` if the keyring is reachable.
  `secrets.Available()` probes; `secrets.Set/Get` are the only blessed
  surfaces.
- Don't store flatpak-builder cache (`.flatpak-builder/`),
  `build-flatpak/`, or `build-flatpak-repo/` in git. They're already in
  `.gitignore`; if you add new build artifacts, mirror that pattern.
- Don't write multi-line docstrings or paragraph-long field/function
  comments. See the Comments convention above — a comment that explains
  WHAT the code does (rather than a non-obvious WHY) is noise to delete,
  not preserve.
