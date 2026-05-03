# Terrain

A native GNOME GUI for [Terraform](https://www.terraform.io/) and
[OpenTofu](https://opentofu.org/), inspired by Terraform Enterprise and
[OTF](https://github.com/leg100/otf).

> **Status:** pre-release. Workspace management, run streaming, plan diff,
> state viewer, variable editing, and remote backends (HCP / TFE / OTF) all
> work. Variable sets management page is in flight; Flathub submission and
> screenshots are pending.

## Features

- **Workspaces** — group local projects and remote backends in one sidebar
- **Runs** — streamed `tofu plan`/`apply` with cancel; remote runs via API polling
- **Plan diff** — TFE-style per-resource changes with action badges (`+`/`~`/`−`/`−/+`) and attribute-level before/after
- **State viewer** — resource tree with attribute display, refreshes via `tofu show -json`
- **Variables** — read/write with sensitive-value masking via the system keyring
- **Run history** — past runs persisted to disk (local) or fetched from API (remote), clickable for read-only replay
- **Hybrid backends** — local CLI runner + remote OTF / HCP Terraform / Terraform Enterprise

## Tech stack

- Go 1.22+ with [gotk4](https://github.com/diamondburned/gotk4) + [gotk4-adwaita](https://github.com/diamondburned/gotk4-adwaita) (GTK 4 / libadwaita 1.4+)
- UI defined in [Blueprint](https://gnome.pages.gitlab.gnome.org/blueprint-compiler/) (`.blp` → `.ui` → gresource)
- HCL parsing: `github.com/hashicorp/hcl/v2`
- Plan/state JSON: `github.com/hashicorp/terraform-json`
- Remote API: `github.com/hashicorp/go-tfe`
- Secrets: `github.com/zalando/go-keyring` (libsecret / Keychain / Credential Vault)
- Build: Meson; distribution: Flatpak (Flathub-ready) and native packaging

## Building from source

### Prerequisites

- Go ≥ 1.22
- Meson ≥ 1.0
- GTK 4 ≥ 4.10 dev headers
- libadwaita ≥ 1.4 dev headers
- blueprint-compiler ≥ 0.10
- (Optional) `tofu` or `terraform` on `PATH` — required for runtime functionality
- (Optional) `xvfb-run` — used by the `meson test` boot smoke test

### Local build

```sh
meson setup build
meson compile -C build
./build/terrain
```

### Sanity checks without a desktop

```sh
./build/terrain --diagnose         # load config + backends, print summary, exit
./build/terrain --debug            # bump log level to debug
meson test -C build                # runs desktop/metainfo validators + xvfb boot smoke
```

### Flatpak

```sh
flatpak install --user org.gnome.Sdk//47 org.gnome.Platform//47 \
                      org.freedesktop.Sdk.Extension.golang//24.08
flatpak-builder --user --install --force-clean build-flatpak \
                build-aux/flatpak/io.github.raspbeguy.Terrain.yml
flatpak run io.github.raspbeguy.Terrain
```

## Project layout

```
cmd/terrain/                  entry point (--diagnose, --debug, --version)
internal/
  domain/                     Backend, Workspace, Run, Variable types — no GTK
  backend/local/              tofu/terraform CLI runner + history
  backend/remote/             go-tfe wrapper for HCP/TFE/OTF
  hcl/                        HCL parse / hclwrite roundtrip
  config/                     XDG TOML registry
  secrets/                    libsecret/Keychain wrapper
  resources/                  embedded gresource bundle
  runner/                     run history (ndjson per workspace)
  ui/
    app.go                    AdwApplication + actions + shortcuts
    window/                   main window controller
    bridge/                   ONLY package crossing domain → GTK via glib.IdleAdd
    dialogs/                  Add Local Project, Add Remote Backend, Edit Variable, Preferences
    views/run/                run detail (log + plan diff)
    views/workspace/          workspace detail (overview / runs / variables / state)
    widgets/                  LogView, PlanDiff, StateTree, VarList
data/                         .desktop, metainfo, gschema, icons, blueprints
build-aux/                    meson scripts, Flatpak manifest, packaging templates
testdata/                     fixtures used by integration tests
```

## Keyboard shortcuts

| Shortcut          | Action                  |
|-------------------|-------------------------|
| `Ctrl+N`          | Add Local Project       |
| `Ctrl+Shift+N`    | Add Remote Backend      |
| `Ctrl+,`          | Preferences             |
| `Ctrl+Q`          | Quit                    |

## License

GPL-3.0-or-later.
