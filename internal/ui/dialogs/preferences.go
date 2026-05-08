package dialogs

import (
	"context"
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/local"
	"github.com/raspbeguy/terrain/internal/config"
	"github.com/raspbeguy/terrain/internal/sshkeys"
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

	managedGroup      *adw.PreferencesGroup
	installManagedBtn *gtk.Button
	cleanManagedBtn   *gtk.Button
	managedBinaryRows []*adw.ActionRow

	sshKeysGroup    *adw.PreferencesGroup
	sshGenerateBtn  *gtk.Button
	sshImportBtn    *gtk.Button
	sshKeyRows      []*adw.ActionRow

	parent *gtk.Window
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
		managedGroup:        uihelpers.MustCast[*adw.PreferencesGroup](builder, "managed_binaries_group"),
		installManagedBtn:   uihelpers.MustCast[*gtk.Button](builder, "install_managed_binary_button"),
		cleanManagedBtn:     uihelpers.MustCast[*gtk.Button](builder, "clean_managed_binaries_button"),
		sshKeysGroup:        uihelpers.MustCast[*adw.PreferencesGroup](builder, "ssh_keys_group"),
		sshGenerateBtn:      uihelpers.MustCast[*gtk.Button](builder, "ssh_keys_generate_button"),
		sshImportBtn:        uihelpers.MustCast[*gtk.Button](builder, "ssh_keys_import_button"),
	}

	p.bindBinaries()
	p.bindEngine(cfg)
	p.bindTheme()
	p.bindBackends(remoteBackends)
	p.bindManagedBinaries()
	p.bindSSHKeys()

	return p
}

func (p *Preferences) Present(parent *gtk.Window) {
	p.parent = parent
	p.dialog.Present(parent)
}

// ConnectClosed proxies the underlying Adw.Dialog signal so callers can refresh dependent UI when the user closes Preferences.
func (p *Preferences) ConnectClosed(fn func()) {
	p.dialog.ConnectClosed(fn)
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

func (p *Preferences) bindSSHKeys() {
	p.sshGenerateBtn.ConnectClicked(func() {
		PresentSSHGenerate(p.parent, func() { p.refreshSSHKeys() })
	})
	p.sshImportBtn.ConnectClicked(func() {
		PresentSSHImport(p.parent, func() { p.refreshSSHKeys() })
	})
	p.refreshSSHKeys()
}

func (p *Preferences) refreshSSHKeys() {
	for _, row := range p.sshKeyRows {
		p.sshKeysGroup.Remove(row)
	}
	p.sshKeyRows = nil

	keys, err := sshkeys.List()
	if err != nil {
		slog.Warn("list ssh keys", "err", err)
		return
	}
	if len(keys) == 0 {
		empty := adw.NewActionRow()
		empty.SetTitle("No keys")
		empty.SetSubtitle("Click Generate to create one, or Import to bring in an existing key.")
		empty.AddCSSClass("dim-label")
		p.sshKeysGroup.Add(empty)
		p.sshKeyRows = append(p.sshKeyRows, empty)
		return
	}
	for _, k := range keys {
		k := k
		row := adw.NewActionRow()
		row.SetTitle(escapeMarkup(k.Label))
		row.SetSubtitle(k.Type + " · " + k.Fingerprint)
		row.AddSuffix(p.sshKeySuffix(k))
		p.sshKeysGroup.Add(row)
		p.sshKeyRows = append(p.sshKeyRows, row)
	}
}

func (p *Preferences) sshKeySuffix(k sshkeys.KeyInfo) *gtk.Box {
	copyBtn := gtk.NewButtonFromIconName("edit-copy-symbolic")
	copyBtn.SetVAlign(gtk.AlignCenter)
	copyBtn.SetTooltipText("Copy public key")
	copyBtn.AddCSSClass("flat")
	copyBtn.ConnectClicked(func() {
		pub, err := sshkeys.PublicKeyText(k.Label)
		if err != nil {
			return
		}
		copyBtn.Display().Clipboard().SetText(pub)
		slog.Info("copied ssh public key", "label", k.Label)
	})

	delBtn := gtk.NewButtonFromIconName("user-trash-symbolic")
	delBtn.SetVAlign(gtk.AlignCenter)
	delBtn.SetTooltipText("Remove " + k.Label)
	delBtn.AddCSSClass("flat")
	delBtn.AddCSSClass("destructive-action")
	delBtn.ConnectClicked(func() {
		if err := sshkeys.Remove(k.Label); err != nil {
			slog.Error("remove ssh key", "label", k.Label, "err", err)
			return
		}
		p.refreshSSHKeys()
	})

	box := gtk.NewBox(gtk.OrientationHorizontal, 4)
	box.SetVAlign(gtk.AlignCenter)
	box.Append(copyBtn)
	box.Append(delBtn)
	return box
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
