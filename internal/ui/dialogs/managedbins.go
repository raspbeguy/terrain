package dialogs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/backend/local"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const managedInstallResource = "/io/github/raspbeguy/Terrain/managed-binary-install.ui"

// PresentManagedInstall: onComplete fires on the GTK main thread after success.
func PresentManagedInstall(parent *gtk.Window, onComplete func()) {
	builder := gtk.NewBuilderFromResource(managedInstallResource)
	dialog := uihelpers.MustCast[*adw.Dialog](builder, "managed_install_dialog")
	engineRow := uihelpers.MustCast[*adw.ComboRow](builder, "managed_install_engine_row")
	versionRow := uihelpers.MustCast[*adw.EntryRow](builder, "managed_install_version_row")
	statusRow := uihelpers.MustCast[*adw.ActionRow](builder, "managed_install_status_row")
	cancelBtn := uihelpers.MustCast[*gtk.Button](builder, "managed_install_cancel_button")
	installBtn := uihelpers.MustCast[*gtk.Button](builder, "managed_install_button")

	cancelBtn.ConnectClicked(func() { dialog.Close() })

	installBtn.ConnectClicked(func() {
		engine := "tofu"
		if engineRow.Selected() == 1 {
			engine = "terraform"
		}
		version := strings.TrimSpace(versionRow.Text())
		if version == "" {
			statusRow.SetSubtitle("✗ version is required")
			return
		}

		statusRow.SetSubtitle("Downloading…")
		installBtn.SetSensitive(false)
		cancelBtn.SetSensitive(false)

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, err := local.InstallManagedBinary(ctx, engine, version)

			glibIdleAdd(func() {
				if err != nil {
					slog.Warn("install managed binary", "engine", engine, "version", version, "err", err)
					statusRow.SetSubtitle("✗ " + err.Error())
					installBtn.SetSensitive(true)
					cancelBtn.SetSensitive(true)
					return
				}
				statusRow.SetSubtitle(fmt.Sprintf("✓ installed %s %s", engine, version))
				if onComplete != nil {
					onComplete()
				}
				dialog.Close()
			})
		}()
	})

	dialog.Present(parent)
}

func FormatManagedBinarySize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGT"[exp])
}
