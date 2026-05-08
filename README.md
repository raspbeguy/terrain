# Terrain

A native GNOME GUI for [Terraform](https://www.terraform.io/) and
[OpenTofu](https://opentofu.org/), inspired by Terraform Enterprise and
[OTF](https://github.com/leg100/otf).

> Workspace management, run streaming, plan diff, state viewer,
> variable editing, variable sets, and remote backends (HCP / TFE / OTF)
> all work. Flathub submission is queued.

## Features

- **Local projects from git** — point at a `git@…` or `https://…` URL + optional subpath; Terrain clones into its own data dir, no `~/code` access required. Multiple subpaths share one clone.
- **Tofu workspaces** — discovered via `tofu workspace list`, surfaced as nested rows under each project; create/delete from the sidebar; runs pinned via `TF_WORKSPACE`
- **Workspaces sidebar** — local projects and remote backends (HCP / TFE / OTF) in one tree
- **Runs** — streamed `tofu plan`/`apply` with cancel; remote runs via API polling
- **Plan diff** — TFE-style per-resource changes with action badges (`+`/`~`/`−`/`−/+`) and attribute-level before/after
- **State viewer** — resource tree with attribute display, history of past versions, side-by-side diff
- **Variables** — read/write with sensitive-value masking via the system keyring; variable sets too
- **Run history** — past runs persisted to disk (local) or fetched from API (remote), clickable for read-only replay
- **Managed binaries** — Terrain downloads + caches official OpenTofu / Terraform releases per workspace (verified against upstream `_SHA256SUMS`); default for new workspaces, no host install required
- **Git auth in-app** — terrain-managed ed25519 SSH keys (generate or import) and libsecret-stored HTTPS tokens, both sandbox-clean
- **Sync from git remote** — one-click `fetch + reset --hard` against the upstream repo

## Install

Each [release](https://github.com/raspbeguy/terrain/releases) ships six
artifacts:

| File | When to use |
|---|---|
| `terrain-x86_64.flatpak` / `terrain-aarch64.flatpak` | Recommended. Works on any modern Linux desktop. `flatpak install --user --bundle ./terrain-x86_64.flatpak` |
| `terrain-linux-amd64-glibc.tar.gz` / `terrain-linux-arm64-glibc.tar.gz` | Host binary. Needs libadwaita ≥ 1.7 (Fedora 41+, Debian 13+, Ubuntu 24.10+, Arch). |
| `terrain-linux-amd64-musl.tar.gz` / `terrain-linux-arm64-musl.tar.gz` | Host binary for musl distros (Alpine, Void, postmarketOS). |

The host binaries dynamically link against GTK 4 / libadwaita / libsecret;
ensure the runtime versions match. The Flatpak bundle carries its own
runtime and works regardless.

## Tech stack

- Go 1.26+ with [gotk4](https://github.com/diamondburned/gotk4) + [gotk4-adwaita](https://github.com/diamondburned/gotk4-adwaita) (GTK 4 / libadwaita 1.6+)
- UI defined in [Blueprint](https://gnome.pages.gitlab.gnome.org/blueprint-compiler/) (`.blp` → `.ui` → gresource)
- HCL parsing: `github.com/hashicorp/hcl/v2`
- Plan/state JSON: `github.com/hashicorp/terraform-json`
- Remote API: `github.com/hashicorp/go-tfe`
- Secrets: `github.com/zalando/go-keyring` (libsecret / Keychain / Credential Vault)
- Build: Meson; distribution: Flatpak (Flathub-ready) and native packaging

## Building from source

### Prerequisites

- Go ≥ 1.26
- Meson ≥ 1.0
- GTK 4 ≥ 4.10 dev headers
- libadwaita ≥ 1.6 dev headers
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
flatpak install --user org.gnome.Sdk//50 org.gnome.Platform//50 \
                      org.freedesktop.Sdk.Extension.golang//25.08
flatpak-builder --user --install --force-clean build-flatpak \
                build-aux/flatpak/io.github.raspbeguy.Terrain.yml
flatpak run io.github.raspbeguy.Terrain
```

## Project layout

```
cmd/terrain/                  entry point (--diagnose, --debug, --version)
internal/
  domain/                     Backend, Workspace, Run, Variable types — no GTK
  backend/local/              tofu/terraform CLI runner + history + managed binaries + tofu-workspace cache
  backend/remote/             go-tfe wrapper for HCP/TFE/OTF
  hcl/                        HCL parse / hclwrite roundtrip
  config/                     XDG TOML registry (project = git_url + ref + subpath)
  gitutils/                   pure-Go go-git wrapper: clone / sync / ls-remote
  sshkeys/                    terrain-managed ed25519 keypairs under $XDG_DATA_HOME
  secrets/                    libsecret/Keychain wrapper (backend tokens + git HTTPS tokens)
  resources/                  embedded gresource bundle
  runner/                     run history (ndjson per workspace)
  ui/
    app.go                    AdwApplication + actions + shortcuts
    window/                   main window controller
    bridge/                   ONLY package crossing domain → GTK via glib.IdleAdd
    dialogs/                  Add Local, Add Remote, Preferences, Edit Variable, Varsets,
                              State Diff, Workspace Settings, Managed Binary Install,
                              SSH Keys, New / Delete Workspace
    views/run/                run detail (log + plan diff)
    views/workspace/          workspace detail (overview / runs / variables / state)
    widgets/                  LogView, PlanDiff, StateTree, VarList
data/                         .desktop, metainfo, gschema, gresource manifest, blueprints
docs/                         GitHub Pages landing site + app icon SVG
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

## Visual identity

The current app icon is a placeholder I threw together. Terrain wants a
proper [GNOME-style app icon][hig] — vector, drawn in the 128×128 GNOME
template, using the standard palette — because the plan is to apply to
[GNOME Circle][circle] once the project is more mature, and Circle expects
HIG-conformant visuals.

If you have design chops and want to help — icon, mockups, anything visual
— PRs and suggestions are very welcome.

[hig]: https://developer.gnome.org/hig/guidelines/app-icons.html
[circle]: https://circle.gnome.org/

## License

GPL-3.0-or-later.
