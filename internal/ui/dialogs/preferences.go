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

type Preferences struct {
	dialog *adw.PreferencesDialog

	themeRow            *adw.ComboRow
	engineRow           *adw.ComboRow
	tofuRow             *adw.ActionRow
	terraformRow        *adw.ActionRow
	remoteBackendsGroup *adw.PreferencesGroup

	runtimePathRow    *adw.EntryRow
	defaultRunModeRow *adw.ComboRow
	tofuImageRow      *adw.EntryRow
	terraformImageRow *adw.EntryRow

	managedGroup      *adw.PreferencesGroup
	installManagedBtn *gtk.Button
	cleanManagedBtn   *gtk.Button
	managedBinaryRows []*adw.ActionRow
	parent            *gtk.Window

	cfg *config.Config
}

type RemoteBackend interface {
	ID() string
	DisplayName() string
	Probe(ctx context.Context)
}

func NewPreferences(cfg *config.Config, remoteBackends []RemoteBackend) *Preferences {
	builder := gtk.NewBuilderFromResource(preferencesResource)
	p := &Preferences{
		dialog:              uihelpers.MustCast[*adw.PreferencesDialog](builder, "preferences_dialog"),
		themeRow:            uihelpers.MustCast[*adw.ComboRow](builder, "theme_row"),
		engineRow:           uihelpers.MustCast[*adw.ComboRow](builder, "engine_row"),
		tofuRow:             uihelpers.MustCast[*adw.ActionRow](builder, "tofu_row"),
		terraformRow:        uihelpers.MustCast[*adw.ActionRow](builder, "terraform_row"),
		remoteBackendsGroup: uihelpers.MustCast[*adw.PreferencesGroup](builder, "remote_backends_group"),
		runtimePathRow:      uihelpers.MustCast[*adw.EntryRow](builder, "runtime_path_row"),
		defaultRunModeRow:   uihelpers.MustCast[*adw.ComboRow](builder, "default_run_mode_row"),
		tofuImageRow:        uihelpers.MustCast[*adw.EntryRow](builder, "tofu_image_row"),
		terraformImageRow:   uihelpers.MustCast[*adw.EntryRow](builder, "terraform_image_row"),
		managedGroup:        uihelpers.MustCast[*adw.PreferencesGroup](builder, "managed_binaries_group"),
		installManagedBtn:   uihelpers.MustCast[*gtk.Button](builder, "install_managed_binary_button"),
		cleanManagedBtn:     uihelpers.MustCast[*gtk.Button](builder, "clean_managed_binaries_button"),
		cfg:                 cfg,
	}

	p.bindBinaries()
	p.bindEngine(cfg)
	p.bindTheme()
	p.bindBackends(remoteBackends)
	p.bindContainerRuntime(cfg)
	p.bindManagedBinaries()

	return p
}

func (p *Preferences) Present(parent *gtk.Window) {
	p.parent = parent
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
}

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

func (p *Preferences) bindContainerRuntime(cfg *config.Config) {
	if cfg == nil {
		return
	}
	p.runtimePathRow.SetText(cfg.App.ContainerRuntimePath)
	p.tofuImageRow.SetText(cfg.App.DefaultImageTofu)
	p.terraformImageRow.SetText(cfg.App.DefaultImageTerraform)
	switch cfg.App.DefaultRunMode {
	case "bubblewrap":
		p.defaultRunModeRow.SetSelected(1)
	case "container":
		p.defaultRunModeRow.SetSelected(2)
	default:
		p.defaultRunModeRow.SetSelected(0)
	}

	p.runtimePathRow.ConnectApply(func() {
		cfg.App.ContainerRuntimePath = p.runtimePathRow.Text()
		p.persist()
	})
	p.tofuImageRow.ConnectApply(func() {
		cfg.App.DefaultImageTofu = p.tofuImageRow.Text()
		p.persist()
	})
	p.terraformImageRow.ConnectApply(func() {
		cfg.App.DefaultImageTerraform = p.terraformImageRow.Text()
		p.persist()
	})
	p.defaultRunModeRow.Connect("notify::selected", func() {
		switch p.defaultRunModeRow.Selected() {
		case 1:
			cfg.App.DefaultRunMode = "bubblewrap"
		case 2:
			cfg.App.DefaultRunMode = "container"
		default:
			cfg.App.DefaultRunMode = "subprocess"
		}
		p.persist()
	})
}

func (p *Preferences) persist() {
	if p.cfg == nil {
		return
	}
	if err := p.cfg.Save(); err != nil {
		slog.Error("save preferences", "err", err)
	}
}

func (p *Preferences) bindManagedBinaries() {
	p.installManagedBtn.ConnectClicked(func() {
		PresentManagedInstall(p.parent, func() { p.refreshManagedBinaries() })
	})
	p.cleanManagedBtn.ConnectClicked(func() {
		removed, err := local.CleanUnusedManagedBinaries()
		if err != nil {
			slog.Error("clean unused managed binaries", "err", err)
			return
		}
		slog.Info("cleaned unused managed binaries", "count", len(removed))
		p.refreshManagedBinaries()
	})
	p.refreshManagedBinaries()
}

func (p *Preferences) refreshManagedBinaries() {
	for _, row := range p.managedBinaryRows {
		p.managedGroup.Remove(row)
	}
	p.managedBinaryRows = nil

	bins, err := local.ListManagedBinaries()
	if err != nil {
		slog.Warn("list managed binaries", "err", err)
		return
	}
	if len(bins) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No managed binaries installed")
		empty.SetSubtitle("Click \"Install version\" to download one.")
		empty.AddCSSClass("dim-label")
		p.managedGroup.Add(empty)
		p.managedBinaryRows = append(p.managedBinaryRows, empty)
		return
	}
	for _, b := range bins {
		row := adw.NewActionRow()
		row.SetTitle(b.Engine + " " + b.Version)
		row.SetSubtitle(FormatManagedBinarySize(b.SizeBytes))
		row.AddSuffix(p.managedRemoveButton(b))
		p.managedGroup.Add(row)
		p.managedBinaryRows = append(p.managedBinaryRows, row)
	}
}

func (p *Preferences) managedRemoveButton(b local.ManagedBinaryInfo) *gtk.Button {
	btn := gtk.NewButtonFromIconName("user-trash-symbolic")
	btn.SetVAlign(gtk.AlignCenter)
	btn.SetTooltipText("Remove " + b.Engine + " " + b.Version)
	btn.AddCSSClass("flat")
	btn.AddCSSClass("destructive-action")
	btn.ConnectClicked(func() {
		if err := local.RemoveManagedBinary(b.Engine, b.Version); err != nil {
			slog.Error("remove managed binary", "engine", b.Engine, "version", b.Version, "err", err)
			return
		}
		p.refreshManagedBinaries()
	})
	return btn
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
