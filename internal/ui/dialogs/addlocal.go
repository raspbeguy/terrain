package dialogs

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/gitutils"
	"github.com/raspbeguy/terrain/internal/secrets"
	"github.com/raspbeguy/terrain/internal/sshkeys"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
)

const addLocalResource = "/io/github/raspbeguy/Terrain/add-local.ui"

type LocalProject struct {
	Name        string
	GitURL      string
	GitRef      string
	Subpath     string
	Username    string
	Token       string
	SSHKeyLabel string
}

func AddLocal(parent *gtk.Window, existingClones []ExistingClone, onSubmitted func(LocalProject), onOpenPreferences func(onClosed func())) {
	builder := gtk.NewBuilderFromResource(addLocalResource)

	dialog := uihelpers.MustCast[*adw.Dialog](builder, "add_local_dialog")
	urlRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_local_url_row")
	refRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_local_ref_row")
	subpathRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_local_subpath_row")
	nameRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_local_name_row")
	reuseRow := uihelpers.MustCast[*adw.ActionRow](builder, "add_local_reuse_row")
	httpsGroup := uihelpers.MustCast[*adw.PreferencesGroup](builder, "add_local_https_group")
	usernameRow := uihelpers.MustCast[*adw.EntryRow](builder, "add_local_username_row")
	tokenRow := uihelpers.MustCast[*adw.PasswordEntryRow](builder, "add_local_token_row")
	sshGroup := uihelpers.MustCast[*adw.PreferencesGroup](builder, "add_local_ssh_group")
	sshKeyRow := uihelpers.MustCast[*adw.ComboRow](builder, "add_local_ssh_key_row")
	manageKeysBtn := uihelpers.MustCast[*gtk.Button](builder, "add_local_manage_keys_button")
	statusRow := uihelpers.MustCast[*adw.ActionRow](builder, "add_local_status_row")
	testBtn := uihelpers.MustCast[*gtk.Button](builder, "add_local_test_button")
	cancelBtn := uihelpers.MustCast[*gtk.Button](builder, "add_local_cancel_button")
	addBtn := uihelpers.MustCast[*gtk.Button](builder, "add_local_add_button")

	keys, _ := sshkeys.List()
	keyLabels := make([]string, 0, len(keys))
	for _, k := range keys {
		keyLabels = append(keyLabels, k.Label)
	}
	repopulateSSHKeys(sshKeyRow, keyLabels)

	updateAuthVisibility := func() {
		ssh := isSSHURL(strings.TrimSpace(urlRow.Text()))
		httpsGroup.SetVisible(!ssh)
		sshGroup.SetVisible(ssh)
	}

	updateReuseHint := func() {
		u := strings.TrimSpace(urlRow.Text())
		ref := strings.TrimSpace(refRow.Text())
		match := false
		for _, ec := range existingClones {
			if ec.GitURL == u && ec.GitRef == ref {
				match = true
				break
			}
		}
		reuseRow.SetVisible(match)
	}

	updateAddSensitivity := func() {
		ok := strings.TrimSpace(urlRow.Text()) != "" &&
			strings.TrimSpace(nameRow.Text()) != ""
		if isSSHURL(strings.TrimSpace(urlRow.Text())) && len(keyLabels) == 0 {
			ok = false
		}
		addBtn.SetSensitive(ok)
	}

	nameAuto := true
	suppressNameChange := false
	setNameAuto := func(s string) {
		suppressNameChange = true
		nameRow.SetText(s)
		suppressNameChange = false
		nameAuto = true
	}
	deriveName := func() {
		if !nameAuto {
			return
		}
		if name := deriveProjectName(urlRow.Text(), subpathRow.Text()); name != "" {
			setNameAuto(name)
		}
	}

	urlRow.ConnectChanged(func() {
		updateAuthVisibility()
		updateReuseHint()
		deriveName()
		updateAddSensitivity()
	})
	refRow.ConnectChanged(func() {
		updateReuseHint()
		updateAddSensitivity()
	})
	subpathRow.ConnectChanged(func() {
		deriveName()
		updateAddSensitivity()
	})
	nameRow.ConnectChanged(func() {
		if !suppressNameChange {
			nameAuto = false
		}
		updateAddSensitivity()
	})

	updateAuthVisibility()
	updateAddSensitivity()

	manageKeysBtn.ConnectClicked(func() {
		if onOpenPreferences == nil {
			return
		}
		onOpenPreferences(func() {
			ks, _ := sshkeys.List()
			keyLabels = keyLabels[:0]
			for _, k := range ks {
				keyLabels = append(keyLabels, k.Label)
			}
			repopulateSSHKeys(sshKeyRow, keyLabels)
			updateAddSensitivity()
		})
	})

	collect := func() (LocalProject, error) {
		p := LocalProject{
			Name:    strings.TrimSpace(nameRow.Text()),
			GitURL:  strings.TrimSpace(urlRow.Text()),
			GitRef:  strings.TrimSpace(refRow.Text()),
			Subpath: strings.Trim(strings.TrimSpace(subpathRow.Text()), "/"),
		}
		if p.GitURL == "" {
			return p, errors.New("git URL is required")
		}
		if p.Name == "" {
			return p, errors.New("display name is required")
		}
		if isSSHURL(p.GitURL) {
			if len(keyLabels) == 0 {
				return p, errors.New("no SSH keys available; add one in Preferences first")
			}
			p.SSHKeyLabel = keyLabels[sshKeyRow.Selected()]
		} else {
			p.Username = strings.TrimSpace(usernameRow.Text())
			p.Token = tokenRow.Text()
		}
		return p, nil
	}

	testBtn.ConnectClicked(func() {
		form, err := collect()
		if err != nil {
			statusRow.SetSubtitle("✗ " + err.Error())
			return
		}
		statusRow.SetSubtitle("Probing…")
		go func() {
			auth, err := buildAuth(form)
			if err != nil {
				updateStatus(statusRow, "✗ "+err.Error())
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			hash, err := gitutils.LsRemote(ctx, form.GitURL, form.GitRef, auth)
			if err != nil {
				slog.Warn("git ls-remote failed", "err", err)
				updateStatus(statusRow, "✗ "+err.Error())
				return
			}
			short := hash
			if len(short) > 8 {
				short = short[:8]
			}
			updateStatus(statusRow, "✓ reachable @ "+short)
		}()
	})

	cancelBtn.ConnectClicked(func() { dialog.Close() })

	addBtn.ConnectClicked(func() {
		form, err := collect()
		if err != nil {
			statusRow.SetSubtitle("✗ " + err.Error())
			return
		}
		if !isSSHURL(form.GitURL) && form.Token != "" {
			if host := urlHost(form.GitURL); host != "" {
				if err := secrets.SetGitToken(host, form.Token); err != nil {
					slog.Warn("save git token", "err", err, "host", host)
				}
			}
		}
		slog.Info("local project submitted", "url", form.GitURL, "ref", form.GitRef, "subpath", form.Subpath)
		dialog.Close()
		onSubmitted(form)
	})

	dialog.Present(parent)
}

type ExistingClone struct {
	GitURL string
	GitRef string
}

func AddSubpathFor(parent *gtk.Window, src ProjectSource, onSubmitted func(LocalProject)) {
	dlg := adw.NewAlertDialog(
		"Add subpath from "+src.Name,
		"Reuses the existing clone of "+src.GitURL+".",
	)

	subpathEntry := gtk.NewEntry()
	subpathEntry.SetPlaceholderText("Subpath inside the repo (e.g. envs/prod)")
	nameEntry := gtk.NewEntry()
	nameEntry.SetPlaceholderText("Display name")

	box := gtk.NewBox(gtk.OrientationVertical, 8)
	box.Append(subpathEntry)
	box.Append(nameEntry)
	dlg.SetExtraChild(box)

	nameAuto := true
	suppressNameChange := false
	subpathEntry.ConnectChanged(func() {
		if !nameAuto {
			return
		}
		if name := deriveProjectName(src.GitURL, subpathEntry.Text()); name != "" {
			suppressNameChange = true
			nameEntry.SetText(name)
			suppressNameChange = false
		}
	})
	nameEntry.ConnectChanged(func() {
		if !suppressNameChange {
			nameAuto = false
		}
	})

	dlg.AddResponse("cancel", "Cancel")
	dlg.AddResponse("add", "Add")
	dlg.SetResponseAppearance("add", adw.ResponseSuggested)
	dlg.SetDefaultResponse("add")
	dlg.SetCloseResponse("cancel")
	dlg.ConnectResponse(func(resp string) {
		if resp != "add" {
			return
		}
		p := LocalProject{
			Name:        strings.TrimSpace(nameEntry.Text()),
			GitURL:      src.GitURL,
			GitRef:      src.GitRef,
			Subpath:     strings.Trim(strings.TrimSpace(subpathEntry.Text()), "/"),
			Username:    src.GitUsername,
			SSHKeyLabel: src.SSHKeyLabel,
		}
		if p.Name == "" {
			p.Name = deriveProjectName(p.GitURL, p.Subpath)
		}
		onSubmitted(p)
	})
	dlg.Present(parent)
}

type ProjectSource struct {
	Name        string
	GitURL      string
	GitRef      string
	GitUsername string
	SSHKeyLabel string
}

func repopulateSSHKeys(row *adw.ComboRow, labels []string) {
	model := gtk.NewStringList(labels)
	row.SetModel(model)
}

func isSSHURL(u string) bool {
	if strings.HasPrefix(u, "ssh://") || strings.HasPrefix(u, "git+ssh://") {
		return true
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "file://") {
		return false
	}
	return strings.Contains(u, "@") && strings.Contains(u, ":")
}

func deriveProjectName(gitURL, subpath string) string {
	if sub := strings.Trim(strings.TrimSpace(subpath), "/"); sub != "" {
		return path.Base(sub)
	}
	u := strings.TrimSpace(gitURL)
	if u == "" {
		return ""
	}
	if isSSHURL(u) && !strings.HasPrefix(u, "ssh://") {
		if idx := strings.LastIndex(u, ":"); idx >= 0 {
			u = u[idx+1:]
		}
	}
	parsed, err := url.Parse(u)
	if err == nil && parsed.Path != "" {
		u = parsed.Path
	}
	u = strings.TrimSuffix(strings.Trim(u, "/"), ".git")
	if u == "" {
		return ""
	}
	return path.Base(u)
}

func sshURLUser(raw string) string {
	if strings.HasPrefix(raw, "ssh://") || strings.HasPrefix(raw, "git+ssh://") {
		if u, err := url.Parse(raw); err == nil && u.User != nil {
			return u.User.Username()
		}
		return ""
	}
	if at := strings.Index(raw, "@"); at > 0 {
		if colon := strings.Index(raw, ":"); colon > at {
			return raw[:at]
		}
	}
	return ""
}

func urlHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

func BuildAuth(p LocalProject) (gitutils.Auth, error) {
	return buildAuth(p)
}

func buildAuth(p LocalProject) (gitutils.Auth, error) {
	if isSSHURL(p.GitURL) {
		if p.SSHKeyLabel == "" {
			return gitutils.NoAuth, errors.New("ssh URL needs an ssh key label")
		}
		path, err := sshkeys.PrivateKeyPath(p.SSHKeyLabel)
		if err != nil {
			return gitutils.NoAuth, err
		}
		return gitutils.SSHKeyAuth(path, sshURLUser(p.GitURL))
	}
	if p.Token != "" {
		return gitutils.HTTPSBasicAuth(p.Username, p.Token), nil
	}
	if host := urlHost(p.GitURL); host != "" {
		if tok, err := secrets.GitToken(host); err == nil && tok != "" {
			return gitutils.HTTPSBasicAuth(p.Username, tok), nil
		}
	}
	return gitutils.NoAuth, nil
}
