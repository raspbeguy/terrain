package dialogs

import (
	"context"
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/local"
	"github.com/raspbeguy/terrain/internal/config"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const preferencesResource = "/io/github/raspbeguy/Terrain/preferences.ui"

// Preferences is the application preferences dialog. Wires the static
// scaffolding: theme picker, default-engine picker, detected-binary display,
// and a per-remote-backend re-probe button.
type Preferences struct {
	dialog *adw.PreferencesDialog

	themeRow            *adw.ComboRow
	engineRow           *adw.ComboRow
	tofuRow             *adw.ActionRow
	terraformRow        *adw.ActionRow
	remoteBackendsGroup *adw.PreferencesGroup
}

// RemoteBackend is the subset of capabilities the preferences dialog
// needs to render the per-backend Re-probe button.
type RemoteBackend interface {
	ID() string
	DisplayName() string
	Probe(ctx context.Context)
}

// NewPreferences loads the dialog from gresource and pre-populates rows from
// the current config and detected binaries. remoteBackends is the live list
// of remote backends to populate the Backends page; pass nil if there are
// none and the page will show an empty state.
func NewPreferences(cfg *config.Config, remoteBackends []RemoteBackend) *Preferences {
	builder := gtk.NewBuilderFromResource(preferencesResource)
	p := &Preferences{
		dialog:              uihelpers.MustCast[*adw.PreferencesDialog](builder, "preferences_dialog"),
		themeRow:            uihelpers.MustCast[*adw.ComboRow](builder, "theme_row"),
		engineRow:           uihelpers.MustCast[*adw.ComboRow](builder, "engine_row"),
		tofuRow:             uihelpers.MustCast[*adw.ActionRow](builder, "tofu_row"),
		terraformRow:        uihelpers.MustCast[*adw.ActionRow](builder, "terraform_row"),
		remoteBackendsGroup: uihelpers.MustCast[*adw.PreferencesGroup](builder, "remote_backends_group"),
	}

	p.bindBinaries()
	p.bindEngine(cfg)
	p.bindTheme()
	p.bindBackends(remoteBackends)

	return p
}

// Present shows the preferences dialog as a child of parent.
func (p *Preferences) Present(parent *gtk.Window) {
	p.dialog.Present(parent)
}

func (p *Preferences) bindBinaries() {
	p.tofuRow.SetSubtitle("Not found")
	p.terraformRow.SetSubtitle("Not found")

	for _, info := range local.DetectAll() {
		switch info.Name {
		case "tofu":
			p.tofuRow.SetSubtitle(info.Path)
		case "terraform":
			p.terraformRow.SetSubtitle(info.Path)
		}
	}
}

func (p *Preferences) bindEngine(cfg *config.Config) {
	if cfg == nil || cfg.App.DefaultEngine == "terraform" {
		p.engineRow.SetSelected(1)
	} else {
		p.engineRow.SetSelected(0)
	}
	// M1 doesn't persist the selection back — gschema wiring is in M3 polish.
}

// bindBackends populates the Backends page with one row per remote backend,
// each with a Re-probe button that calls backend.Probe in a goroutine
// (network call). Empty list shows a placeholder.
func (p *Preferences) bindBackends(backends []RemoteBackend) {
	if len(backends) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No remote backends")
		empty.SetSubtitle("Add an OTF / HCP / TFE backend from the main menu first.")
		empty.AddCSSClass("dim-label")
		p.remoteBackendsGroup.Add(empty)
		return
	}
	for _, b := range backends {
		b := b
		row := adw.NewActionRow()
		row.SetTitle(escapeMarkup(b.DisplayName()))
		row.SetSubtitle(b.ID())

		btn := gtk.NewButtonWithLabel("Re-probe")
		btn.SetVAlign(gtk.AlignCenter)
		btn.AddCSSClass("flat")
		btn.ConnectClicked(func() {
			slog.Info("re-probe backend", "id", b.ID())
			btn.SetSensitive(false)
			go func() {
				b.Probe(context.Background())
				glibIdleAdd(func() { btn.SetSensitive(true) })
			}()
		})
		row.AddSuffix(btn)

		p.remoteBackendsGroup.Add(row)
	}
}

func (p *Preferences) bindTheme() {
	mgr := adw.StyleManagerGetDefault()
	switch mgr.ColorScheme() {
	case adw.ColorSchemeForceLight, adw.ColorSchemeDefault:
		p.themeRow.SetSelected(1)
	case adw.ColorSchemeForceDark, adw.ColorSchemePreferDark:
		p.themeRow.SetSelected(2)
	default:
		p.themeRow.SetSelected(0)
	}

	p.themeRow.Connect("notify::selected", func() {
		switch p.themeRow.Selected() {
		case 1:
			mgr.SetColorScheme(adw.ColorSchemeForceLight)
		case 2:
			mgr.SetColorScheme(adw.ColorSchemeForceDark)
		default:
			mgr.SetColorScheme(adw.ColorSchemeDefault)
		}
	})
}
