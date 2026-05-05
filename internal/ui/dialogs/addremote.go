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

type RemoteForm struct {
	Name         string
	Flavor       remote.Flavor
	Endpoint     string
	Organization string
	Token        string
}

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

	// HCP has a fixed endpoint; TFE/OTF need an explicit URL.
	updateEndpointVisibility := func() {
		switch flavorIndex(flavorRow) {
		case 0:
			endpointRow.SetVisible(false)
		default:
			endpointRow.SetVisible(true)
		}
	}
	updateEndpointVisibility()

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

// updateStatus marshals to the GTK main thread; one-shot, doesn't fit bridge's stream shape.
func updateStatus(row *adw.ActionRow, msg string) {
	glibIdleAdd(func() { row.SetSubtitle(msg) })
}

