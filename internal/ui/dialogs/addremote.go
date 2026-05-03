package dialogs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/remote"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const addRemoteResource = "/io/github/raspbeguy/Terrain/add-remote.ui"

// RemoteForm is the validated payload returned to the caller after the user
// hits Add. Callers persist it via config.AddRemoteBackend.
type RemoteForm struct {
	Name         string
	Flavor       remote.Flavor
	Endpoint     string
	Organization string
	Token        string
}

// AddRemote presents the Add Remote Backend dialog. onSubmitted runs once
// the user accepts. Cancellation drops silently.
func AddRemote(parent *gtk.Window, onSubmitted func(RemoteForm)) {
	builder := gtk.NewBuilderFromResource(addRemoteResource)

	dialog := uihelpers.MustCast[*adw.Dialog](builder, "add_remote_dialog")
	flavorRow := uihelpers.MustCast[*adw.ComboRow](builder, "add_remote_flavor_row")
	nameRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_remote_name_row")
	endpointRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_remote_endpoint_row")
	orgRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_remote_org_row")
	tokenRow := uihelpers.MustCast[*adw.PasswordEntryRow](builder, "add_remote_token_row")
	statusRow := uihelpers.MustCast[*adw.ActionRow](builder, "add_remote_status_row")
	testBtn := uihelpers.MustCast[*gtk.Button](builder, "add_remote_test_button")
	cancelBtn := uihelpers.MustCast[*gtk.Button](builder, "add_remote_cancel_button")
	addBtn := uihelpers.MustCast[*gtk.Button](builder, "add_remote_add_button")

	// Endpoint visibility is driven by flavor: HCP has a fixed endpoint;
	// TFE/OTF require an explicit URL.
	updateEndpointVisibility := func() {
		switch flavorIndex(flavorRow) {
		case 0: // HCP
			endpointRow.SetVisible(false)
		default: // TFE, OTF
			endpointRow.SetVisible(true)
		}
	}
	updateEndpointVisibility()

	// Form completeness check — Add button enabled only when minimal fields
	// are populated.
	updateAddSensitivity := func() {
		ok := strings.TrimSpace(nameRow.Text()) != "" &&
			strings.TrimSpace(orgRow.Text()) != "" &&
			tokenRow.Text() != ""
		if flavorIndex(flavorRow) != 0 {
			ok = ok && strings.TrimSpace(endpointRow.Text()) != ""
		}
		addBtn.SetSensitive(ok)
	}

	flavorRow.Connect("notify::selected", func() {
		updateEndpointVisibility()
		updateAddSensitivity()
	})
	nameRow.ConnectChanged(updateAddSensitivity)
	endpointRow.ConnectChanged(updateAddSensitivity)
	orgRow.ConnectChanged(updateAddSensitivity)
	tokenRow.ConnectChanged(updateAddSensitivity)

	collect := func() (RemoteForm, error) {
		form := RemoteForm{
			Name:         strings.TrimSpace(nameRow.Text()),
			Flavor:       flavorFromIndex(flavorIndex(flavorRow)),
			Endpoint:     strings.TrimSpace(endpointRow.Text()),
			Organization: strings.TrimSpace(orgRow.Text()),
			Token:        tokenRow.Text(),
		}
		if form.Name == "" {
			return form, errors.New("display name is required")
		}
		if form.Organization == "" {
			return form, errors.New("organization is required")
		}
		if form.Token == "" {
			return form, errors.New("API token is required")
		}
		if form.Flavor != remote.FlavorHCP && form.Endpoint == "" {
			return form, errors.New("endpoint URL is required for self-hosted backends")
		}
		return form, nil
	}

	testBtn.ConnectClicked(func() {
		form, err := collect()
		if err != nil {
			statusRow.SetSubtitle("✗ " + err.Error())
			return
		}
		slog.Info("test remote connection", "flavor", form.Flavor, "endpoint", form.Endpoint, "org", form.Organization)
		statusRow.SetSubtitle("Connecting…")

		go func() {
			b, err := remote.New(remote.Config{
				ID:           "test",
				Name:         form.Name,
				Flavor:       form.Flavor,
				Endpoint:     form.Endpoint,
				Organization: form.Organization,
				Token:        form.Token,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var resultMsg string
			if err != nil {
				resultMsg = "✗ " + err.Error()
				slog.Warn("remote test failed at construction", "err", err)
			} else if err := b.TestConnection(ctx); err != nil {
				resultMsg = "✗ " + err.Error()
				slog.Warn("remote test connection failed", "err", err)
			} else {
				resultMsg = "✓ connected to " + form.Organization
				slog.Info("remote test connection ok", "org", form.Organization)
			}
			// Marshal back to UI thread — but we're not pumping through bridge
			// for one-off async UI updates. Use glib.IdleAdd directly here.
			updateStatus(statusRow, resultMsg)
		}()
	})

	cancelBtn.ConnectClicked(func() { dialog.Close() })

	addBtn.ConnectClicked(func() {
		form, err := collect()
		if err != nil {
			statusRow.SetSubtitle("✗ " + err.Error())
			return
		}
		slog.Info("remote backend submitted", "name", form.Name, "flavor", form.Flavor, "org", form.Organization)
		dialog.Close()
		onSubmitted(form)
	})

	dialog.Present(parent)
}

func flavorIndex(row *adw.ComboRow) uint {
	return row.Selected()
}

func flavorFromIndex(i uint) remote.Flavor {
	switch i {
	case 0:
		return remote.FlavorHCP
	case 1:
		return remote.FlavorTFE
	case 2:
		return remote.FlavorOTF
	}
	return remote.FlavorHCP
}

// updateStatus marshals a string update to the GTK main thread. We don't
// route through bridge here because this is a one-shot interaction (Test
// Connection's reply) rather than a streaming domain event.
func updateStatus(row *adw.ActionRow, msg string) {
	// We need glib.IdleAdd from within an async closure. The bridge package
	// would be the canonical place; for a single update we inline rather
	// than expanding bridge's surface for non-domain events.
	idleSetSubtitle(row, msg)
}

func idleSetSubtitle(row *adw.ActionRow, msg string) {
	// Use the gotk4 glib package directly. Imported via blank to avoid
	// pulling glib symbols into this file's namespace; the bridge package
	// is the only "approved" location, but this is a contained one-off.
	glibIdleAdd(func() { row.SetSubtitle(msg) })
}

// glibIdleAdd is wired in addremote_idle.go to keep the import isolated.

