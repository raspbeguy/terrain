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
	progressBar := uihelpers.MustCast[*gtk.ProgressBar](builder, "managed_install_progress")
	cancelBtn := uihelpers.MustCast[*gtk.Button](builder, "managed_install_cancel_button")
	installBtn := uihelpers.MustCast[*gtk.Button](builder, "managed_install_button")

	cancelBtn.ConnectClicked(func() { dialog.Close() })

	installBtn.ConnectClicked(func() {
		engine := "tofu"
		if engineRow.Selected() == 1 {
			engine = "terraform"
		}
		version := strings.TrimSpace(versionRow.Text())

		statusRow.SetSubtitle("Resolving…")
		progressBar.SetVisible(false)
		progressBar.SetFraction(0)
		installBtn.SetSensitive(false)
		cancelBtn.SetSensitive(false)

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			resolved := version
			if resolved == "" {
				glibIdleAdd(func() { statusRow.SetSubtitle("Resolving latest…") })
				latest, lerr := local.LatestManagedVersion(ctx, engine)
				if lerr != nil {
					glibIdleAdd(func() {
						statusRow.SetSubtitle("✗ " + lerr.Error())
						installBtn.SetSensitive(true)
						cancelBtn.SetSensitive(true)
					})
					return
				}
				resolved = latest
			}

			glibIdleAdd(func() {
				statusRow.SetSubtitle(fmt.Sprintf("Downloading %s %s…", engine, resolved))
				progressBar.SetVisible(true)
				progressBar.SetFraction(0)
				progressBar.SetText("")
			})

			progress := func(written, total int64) {
				glibIdleAdd(func() {
					if total > 0 {
						progressBar.SetFraction(float64(written) / float64(total))
						progressBar.SetText(fmt.Sprintf("%s / %s", FormatManagedBinarySize(written), FormatManagedBinarySize(total)))
					} else {
						progressBar.Pulse()
						progressBar.SetText(FormatManagedBinarySize(written))
					}
				})
			}

			_, err := local.InstallManagedBinaryWithProgress(ctx, engine, resolved, progress)

			glibIdleAdd(func() {
				if err != nil {
					slog.Warn("install managed binary", "engine", engine, "version", resolved, "err", err)
					statusRow.SetSubtitle("✗ " + err.Error())
					progressBar.SetVisible(false)
					installBtn.SetSensitive(true)
					cancelBtn.SetSensitive(true)
					return
				}
				progressBar.SetFraction(1)
				statusRow.SetSubtitle(fmt.Sprintf("✓ installed %s %s", engine, resolved))
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
