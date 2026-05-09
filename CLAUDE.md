# Terrain: agent guide

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

# Flatpak: always ~1h, every time. ccache + warm .flatpak-builder/ cache
# do NOT speed up subsequent builds in practice on this VM; the gotk4 cgo
# build dominates and runs from scratch each time. Quote ~60 min ETA when
# planning, regardless of cache state. --force-clean is still required so
# flatpak-builder doesn't refuse to start.
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

   The only sanctioned exception is `internal/ui/dialogs/addremote_idle.go`,
   a contained `glib.IdleAdd` wrapper used by the Add Remote dialog's
   one-shot Test Connection callback. New code should prefer `bridge`; only
   reach for the dialogs-local pattern when streaming through a `RunStream`
   would be obvious overkill for a single async result.

3. **The `Backend` interface** in `internal/domain/backend.go` is the
   architectural fulcrum. Local + remote both implement it; UI never
   branches on backend kind. Optional capabilities (`stateLoader`,
   `stateVersionLister`, `runListing`, `variableLoader`,
   `variableUpserter`) are type-asserted by the window; backends declare
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
   site routes through `internal/backend/local/sandbox.go:hostCommand`,
   wrapped by `runCommand` in `runtime.go`. Inside Flatpak it falls back
   to `flatpak-spawn --host` for host-only paths; the Flathub manifest
   doesn't grant the talk-name for that, so only managed-binary
   subprocess mode works there. Container/bubblewrap modes were
   removed: they were untested, expanded the threat surface, and made
   Flathub review fragile. **Do not** add new exec sites for
   `tofu`/`terraform` that bypass `runCommand`.

7. **Binary resolution** is decoupled from the runtime via the
   `BinaryResolver` interface in `internal/backend/local/binary.go`.
   `pathResolver` discovers `tofu` / `terraform` on host PATH;
   `managedResolver` downloads + caches official releases under
   `$XDG_DATA_HOME/terrain/binaries/<engine>/<version>/` and verifies
   them against the upstream `_SHA256SUMS` file. The choice is per-
   workspace via `WorkspaceSettings.BinarySource`; zero value resolves
   to `managed` (via `BinarySource.Effective()`); explicit `host` uses
   PATH; `managed` pulls the version named by
   `WorkspaceSettings.ManagedEngine` + `ManagedVersion`, falling back
   to `AppConfig.DefaultEngine` via `EffectiveManagedEngine` when
   ManagedEngine is empty. The shared resolver singleton
   (`sharedManagedResolver()`) means UI-triggered installs in
   Preferences and run-time resolutions in `runWorker` use the same
   per-(engine, version) install lock. Workspace overview shows a
   banner when the effective managed engine has no installed binary
   yet.

8. **Local projects are clone-backed, never arbitrary host paths**. A
   `local.Project` carries `(GitURL, GitRef, Subpath)`; `WorkingDir()`
   resolves to `$XDG_DATA_HOME/terrain/git-repos/<hash>/<subpath>` where
   `<hash>` = first 16 hex of `sha256(url + "@" + ref)`. Multiple
   projects with the same `(url, ref)` share one clone; only the
   subpath varies. Clones are app-managed working copies, not user
   editing surfaces; `Sync` is `fetch + reset --hard`, so local edits
   inside the clone are discarded by design. Cloning, syncing, and
   ls-remote probes all go through `internal/gitutils/` (pure-Go
   go-git, never host `git`). Auth uses libsecret tokens for HTTPS or
   `internal/sshkeys` for SSH; both sandbox-local. This is what lets
   the Flathub manifest ship without `--filesystem=home` or
   `--talk-name=org.freedesktop.Flatpak`.

9. **Tofu workspaces are first-class**. One `domain.Workspace` per
    `tofu workspace` (default + any extra). Discovery is dynamic
    (never persisted in `config.toml`): `RefreshWorkspaces` runs
    `tofu workspace list -no-color`, falling back to scanning
    `terraform.tfstate.d/` and finally to `["default"]`. Cache lives
    on `*local.Backend` (`wsCache map[projectID][]string`); `Workspaces()`
    reads it. Refresh fires on app startup (one goroutine per project),
    after every run, after sync from git, and on user demand via the
    project-header kebab. Run pipeline pins each invocation by
    appending `TF_WORKSPACE=<ws.Name>` to `extraEnv` at the single
    chokepoint in `run.go`. New / delete workspace go through
    `Backend.CreateTofuWorkspace` / `DeleteTofuWorkspace`, which
    auto-init the clone if needed.

## Package layout

```
cmd/terrain/                main, --diagnose, --debug, --version flags
internal/
  domain/                   Backend interface, Workspace (incl. GitURL/
                            GitRef/Subpath), Run, Variable, VariableSet,
                            StateVersion, RunStream, etc. NO gotk4 imports.
  backend/local/            tofu/terraform CLI runner. exec.go (subprocess
                            line streaming + SIGINT cancel), run.go (run
                            worker, TF_WORKSPACE injection), runtime.go
                            (runCommand: thin wrapper over hostCommand),
                            wssettings.go (per-workspace settings.json +
                            Effective helpers for default-managed),
                            wslist.go (tofu-workspace cache,
                            RefreshWorkspaces / Create / Delete), wsname.go
                            (validator), gitrepo.go (clone hash, GC),
                            variables.go, varsets.go, snapshot.go,
                            materialize.go, envindex.go, sandbox.go,
                            managedbin.go, latestversion.go, cleanup.go.
  backend/remote/           hashicorp/go-tfe wrapper. backend.go, run.go,
                            variables.go, state.go, compat.go.
  hcl/                      hashicorp/hcl/v2 helpers (parse + hclwrite).
  config/                   $XDG_CONFIG_HOME/terrain/config.toml registry.
                            ProjectConfig is git-shaped: GitURL + GitRef +
                            Subpath + SSHKeyLabel + GitUsername.
  gitutils/                 pure-Go go-git wrapper: Clone, Sync, LsRemote,
                            HTTPSBasicAuth, SSHKeyAuth.
  sshkeys/                  terrain-managed ed25519 keypairs at
                            $XDG_DATA_HOME/terrain/ssh-keys/<label>/.
  secrets/                  zalando/go-keyring wrapper. TokenKey for
                            backend tokens, GitTokenKey for HTTPS git
                            credentials scoped per host.
  runner/                   history.go (ndjson run log), locks.go
                            (per-workspace mutex registry).
  resources/                gresource bundle embedded via go:embed; build
                            tag `embed_gresource` toggles populated vs empty.
  ui/
    app.go                  AdwApplication, gio.SimpleActions, accelerators,
                            workspace-discovery goroutines on activate.
    window/                 main window controller, sidebar with project
                            headers + nested tofu-workspace rows, kebab
                            split (project actions on header, workspace
                            actions on row).
    bridge/                 PumpRun: domain channels → GTK main thread.
    dialogs/                addlocal (form: URL/ref/subpath/auth + Test),
                            addremote, addremote_idle (one-shot Test),
                            preferences (incl. SSH Keys + Binaries pages),
                            sshkeys (generate/import), workspace
                            (New Workspace alert), wssettings (per-workspace
                            binary source), varedit, varsets, statediff,
                            managedbins (install dialog with progress bar).
    views/run/              run detail (log + plan diff tabs).
    views/workspace/        workspace detail (overview with Repository row
                            + sync/open buttons + binary banner; runs;
                            variables; state).
    widgets/                LogView, PlanDiff, StateTree, StateDiff, VarList.
    uihelpers/              MustCast for GtkBuilder objects.
data/
  ui/blueprints/            *.blp source files (12): window, workspace-
                            detail, workspace-settings, run-detail,
                            preferences, add-remote, add-local,
                            ssh-key-import, var-edit, varsets,
                            state-diff, managed-binary-install.
  io.github.raspbeguy.Terrain.{desktop.in, metainfo.xml.in, gschema.xml,
                                gresource.xml}
docs/                       GitHub Pages landing site + the app icon SVG
                            (installed by meson into the icon theme).
build-aux/
  flatpak/                  Flatpak manifest + flatpak-go-mod-generated
                            vendor sources. Targets GNOME 50 + golang Sdk
                            extension 25.08. Manifest sets
                            GOTOOLCHAIN=local; finish-args: ipc, network,
                            wayland/X11, dri, talk-name=org.freedesktop.secrets.
  meson/                    go-build.sh + smoke.sh.
testdata/                   Fixtures used by integration tests.
```

## Conventions

- **Comments (LOAD-BEARING RULE, READ THIS FIRST)**: default to none.
  This is non-negotiable: in repeated sessions, ignoring it has forced
  full git-history rewrites to scrub verbose comments. Apply it on every
  diff, before showing your work, every time.

  - Add a comment **only** when the WHY is non-obvious: a hidden
    invariant, a subtle constraint, a workaround for a specific bug.
  - **One short line max.** If it wraps to a second line in the editor
    at standard widths, it's too long: tighten it or delete it.
  - **No** multi-paragraph docstrings. **No** field-by-field prose for
    self-explanatory struct fields. **No** restating function names in
    prose ("Foo bars the baz"); the signature says it.
  - **No** speculation about future use ("UI hooks pass a context they
    can cancel from a Stop button"). **No** task / fix / date references
    ("verifies the 2026-05-03 change", "added for the X flow"); that
    belongs in commit messages and rots in code.
  - Self-audit pass before every commit:
    `awk '/^[[:space:]]*\/\//{c++} !/^[[:space:]]*\/\//{if(c>=2)print FILENAME":"NR-c": "c" lines"; c=0}' <files>`
    Any hit = trim before committing.

- **Blueprint vs Go**: static layouts (windows, dialogs, page skeletons)
  live in `.blp` files; dynamic content (list rows, dropdown contents,
  per-resource diff rows) is built in Go via `gtk.SignalListItemFactory`
  or direct widget construction. The MustCast helper at
  `internal/ui/uihelpers/builder.go` panics with a clear message on
  builder-ID drift; failures are loud at startup, not silent at runtime.

- **Error handling**: domain errors are sentinels (`domain.ErrNotFound`,
  `domain.ErrNotImplemented`); compare with `errors.Is`. Backend
  implementations wrap with `%w` to preserve the chain. UI shows toast
  errors via `Window.ToastError(msg)` for user-actionable failures and
  logs via `slog` for diagnostic-only ones.

- **slog levels**: `Info` for state transitions visible in the UI (run
  started, variable saved, backend connected). `Debug` for click handlers
  and per-event spam, gated by `--debug` or `TERRAIN_DEBUG=1`. `Warn` for
  recoverable failures the user might want to know about. `Error` for
  things the user definitely needs to see (almost always paired with a
  Toast).

- **File paths**:
  - `$XDG_CONFIG_HOME/terrain/config.toml`: backend registry (durable)
  - `$XDG_CONFIG_HOME/terrain/varsets/<id>.json`: variable set manifests
  - `$XDG_DATA_HOME/terrain/git-repos/<hash>/`: clone of a local project
    repo. `<hash>` = first 16 hex of `sha256(git_url + "@" + git_ref)`;
    multiple subpaths share one clone. Sync = fetch + reset --hard.
  - `$XDG_DATA_HOME/terrain/ssh-keys/<label>/`: terrain-managed ed25519
    keys for SSH-flavoured git URLs. Stays in-sandbox so the Flatpak
    needs no `--socket=ssh-auth` or `--filesystem=~/.ssh`.
  - `$XDG_CACHE_HOME/terrain/<backend>/<ws>/runs/<id>/`: run artifacts
    (ephemeral; deleted by cache cleanup is fine)
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/state-versions/<id>/`: state
    snapshots (durable; retention: keep newest 50 + last 30 days)
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/overrides.tfvars`: terrain-
    managed plain (non-sensitive) variable values. Loaded with the
    project's own tfvars at run-materialize time and passed via
    `-var-file=` so it overrides the project's defaults. Living outside
    the project tree means terrain-managed values can never be
    accidentally committed to the user's repo.
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/env-vars.json`: env-category
    variable name index (names only; values in keyring). Same out-of-
    project rationale as overrides.tfvars.
  - `$XDG_DATA_HOME/terrain/<backend>/<ws>/settings.json`: per-workspace
    overrides: `binary_source`, `managed_engine`, `managed_track_latest`,
    `managed_version`. Zero values inherit: `binary_source` → managed
    (via `BinarySource.Effective()`); `managed_engine` →
    `AppConfig.DefaultEngine`. Edited via the gear button in the
    workspace overview header.

- **Secrets**: never plaintext on disk if avoidable. Sensitive variable
  values + remote backend tokens go to the system keyring (libsecret on
  Linux via go-keyring). Plaintext fallback in `config.toml` is allowed
  when keyring is unreachable, with a logged warning. Run-time materializer
  pulls sensitive values from the keyring into a per-run
  `vars.auto.tfvars` (0600 perms; HCL preserves cty types so list/object
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
  compile cache** for everything that transitively imports them, which
  includes gotk4. Adding dependencies that bump these is expensive.

- **GNOME 47 is EOL** (since Oct 2025); GNOME 49 is the previous-stable
  cycle. The Flatpak manifest targets GNOME 50 (released March 2026,
  current latest). The golang Sdk extension at branch 25.08 (paired with
  fdo SDK 25.08) ships Go 1.26.x, matching `go.mod`'s directive, so the
  manifest sets `GOTOOLCHAIN=local` to refuse network toolchain fetches.

- **Flatpak module sources are vendored** (`build-aux/flatpak/go.mod.yml`
  generated by `flatpak-go-mod`) so the build is offline and Flathub-eligible.
  Regenerate the vendor file after dependency bumps.

## Future ideas

- **Kubernetes runner backend.** A new `internal/backend/k8s/` (sibling to
  `local` and `remote`) that targets the user's existing kubeconfig
  context: each `tofu plan/apply` becomes a `Job` running an
  OpenTofu/Terraform image, with logs streamed from the pod. Deferred:
  the local backend covers the desktop use case and the runtime layer
  was deliberately simplified back to pure subprocess; revisit when
  there's demand and a clean execution-isolation story that doesn't
  need extra Flatpak permissions (kube-apiserver TLS auth + the user's
  kubeconfig already gives us everything we need over plain HTTPS).

## Where to read more

- `/home/alpine/.claude/plans/let-s-develop-a-gnome-melodic-lightning.md`:
  the original architectural plan, milestones M0–M5, every divergence
  documented in the plan-vs-actual audit.

- `/home/alpine/.claude/projects/-home-alpine-repo-terrain/memory/`:
  auto-saved memory files: project overview, build environment, gotk4
  gotchas, Flatpak runtime versions.

- `README.md`: user-facing description + install instructions.

## Don't (load-bearing)

- Don't add new `glib.IdleAdd` call sites outside `internal/ui/bridge/`
  unless you have the same justification as `dialogs/addremote_idle.go`
  (one-shot async result that doesn't fit the stream-of-events shape).
  The default is bridge; divergences need a comment explaining why.
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
  comments. See the Comments LOAD-BEARING RULE above; this has been
  flagged by the user multiple times and required full history rewrites
  to undo. Self-audit before every commit; one short line max or delete.
