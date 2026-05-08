// Package gitutils wraps go-git so Terrain can clone, sync, and probe repos without invoking host git.
package gitutils

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type Auth struct{ method transport.AuthMethod }

var NoAuth = Auth{}

// HTTPSBasicAuth — most forges accept a PAT in the password slot with any non-empty username.
func HTTPSBasicAuth(username, token string) Auth {
	if username == "" {
		username = "git"
	}
	return Auth{method: &githttp.BasicAuth{Username: username, Password: token}}
}

// SSHKeyAuth: user must match the URL's "user@host" — go-git's PublicKeys.User overrides the URL.
func SSHKeyAuth(privateKeyPath, user string) (Auth, error) {
	signer, err := loadSigner(privateKeyPath)
	if err != nil {
		return Auth{}, err
	}
	if user == "" {
		user = "git"
	}
	a := &gitssh.PublicKeys{User: user, Signer: signer}
	a.HostKeyCallback = gossh.InsecureIgnoreHostKey()
	return Auth{method: a}, nil
}

func loadSigner(path string) (gossh.Signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := gossh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key %s: %w", path, err)
	}
	return signer, nil
}

// Clone fetches url@ref into dir; empty ref = remote default branch.
func Clone(ctx context.Context, url, ref, dir string, auth Auth) error {
	opts := &git.CloneOptions{
		URL:  url,
		Auth: auth.method,
	}
	if ref != "" {
		opts.ReferenceName = resolveRef(ref)
		opts.SingleBranch = true
	}
	if _, err := git.PlainCloneContext(ctx, dir, false, opts); err != nil {
		return fmt.Errorf("clone %s: %w", url, err)
	}
	return nil
}

// Sync fetches origin and hard-resets to it; local edits inside the clone are discarded by design.
func Sync(ctx context.Context, dir, ref string, auth Auth) error {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	branch := ref
	if branch == "" {
		b, err := defaultBranch(ctx, repo, auth)
		if err != nil {
			return err
		}
		branch = b
	}

	refSpec := config.RefSpec(fmt.Sprintf(
		"+refs/heads/%s:refs/remotes/origin/%s", branch, branch))
	if err := repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth.method,
		RefSpecs:   []config.RefSpec{refSpec},
		Force:      true,
	}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		// Branch may be a tag; retry once with the tag refspec before giving up.
		tagSpec := config.RefSpec(fmt.Sprintf(
			"+refs/tags/%s:refs/tags/%s", branch, branch))
		if tagErr := repo.FetchContext(ctx, &git.FetchOptions{
			RemoteName: "origin",
			Auth:       auth.method,
			RefSpecs:   []config.RefSpec{tagSpec},
			Force:      true,
		}); tagErr != nil && !errors.Is(tagErr, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("fetch %s: %w", branch, err)
		}
	}

	target, err := repo.ResolveRevision(plumbing.Revision("origin/" + branch))
	if err != nil {
		t, terr := repo.ResolveRevision(plumbing.Revision(branch))
		if terr != nil {
			return fmt.Errorf("resolve %s: %w", branch, err)
		}
		target = t
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: *target}); err != nil {
		return fmt.Errorf("reset --hard %s: %w", target, err)
	}
	return nil
}

// LsRemote returns the resolved commit hash for ref (or HEAD when empty).
func LsRemote(ctx context.Context, url, ref string, auth Auth) (string, error) {
	rem := git.NewRemote(nil, &config.RemoteConfig{
		Name: "probe",
		URLs: []string{url},
	})
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth.method})
	if err != nil {
		return "", fmt.Errorf("ls-remote %s: %w", url, err)
	}

	want := resolveRef(ref)
	for _, r := range refs {
		if ref == "" && r.Name() == plumbing.HEAD {
			return r.Hash().String(), nil
		}
		if ref != "" && (r.Name() == want ||
			r.Name().Short() == ref ||
			r.Name() == plumbing.NewBranchReferenceName(ref) ||
			r.Name() == plumbing.NewTagReferenceName(ref)) {
			return r.Hash().String(), nil
		}
	}
	if ref == "" {
		return "", fmt.Errorf("remote %s has no HEAD", url)
	}
	return "", fmt.Errorf("ref %q not found at %s", ref, url)
}

// HeadCommit returns the current HEAD hash of an existing clone.
func HeadCommit(dir string) (string, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", dir, err)
	}
	h, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("head %s: %w", dir, err)
	}
	return h.Hash().String(), nil
}

func resolveRef(ref string) plumbing.ReferenceName {
	if ref == "" {
		return ""
	}
	return plumbing.NewBranchReferenceName(ref)
}

func defaultBranch(ctx context.Context, repo *git.Repository, auth Auth) (string, error) {
	rem, err := repo.Remote("origin")
	if err != nil {
		return "", fmt.Errorf("origin remote: %w", err)
	}
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth.method})
	if err != nil {
		return "", fmt.Errorf("ls-remote origin: %w", err)
	}
	var headTarget plumbing.ReferenceName
	for _, r := range refs {
		if r.Name() == plumbing.HEAD && r.Type() == plumbing.SymbolicReference {
			headTarget = r.Target()
			break
		}
	}
	if headTarget == "" {
		return "main", nil
	}
	return headTarget.Short(), nil
}
