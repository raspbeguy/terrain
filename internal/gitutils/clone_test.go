package gitutils_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raspbeguy/terrain/internal/gitutils"
)

func TestCloneAndSync(t *testing.T) {
	t.Parallel()

	upstream := newFixtureRepo(t, "hello\n")
	dst := filepath.Join(t.TempDir(), "clone")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := gitutils.Clone(ctx, "file://"+upstream, "main", dst, gitutils.NoAuth); err != nil {
		t.Fatalf("clone: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dst, "README"))
	if err != nil {
		t.Fatalf("read clone: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("clone content = %q, want %q", got, "hello\n")
	}

	addCommit(t, upstream, "README", "hello again\n", "second commit")

	if err := gitutils.Sync(ctx, dst, "main", gitutils.NoAuth); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dst, "README"))
	if err != nil {
		t.Fatalf("read after sync: %v", err)
	}
	if string(got) != "hello again\n" {
		t.Fatalf("synced content = %q, want %q", got, "hello again\n")
	}
}

func TestSyncDiscardsLocalEdits(t *testing.T) {
	t.Parallel()

	upstream := newFixtureRepo(t, "untouched\n")
	dst := filepath.Join(t.TempDir(), "clone")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := gitutils.Clone(ctx, "file://"+upstream, "main", dst, gitutils.NoAuth); err != nil {
		t.Fatalf("clone: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dst, "README"), []byte("locally edited\n"), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}

	if err := gitutils.Sync(ctx, dst, "main", gitutils.NoAuth); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "README"))
	if err != nil {
		t.Fatalf("read after sync: %v", err)
	}
	if string(got) != "untouched\n" {
		t.Fatalf("local edit not discarded: got %q", got)
	}
}

func TestLsRemote(t *testing.T) {
	t.Parallel()

	upstream := newFixtureRepo(t, "x\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hash, err := gitutils.LsRemote(ctx, "file://"+upstream, "main", gitutils.NoAuth)
	if err != nil {
		t.Fatalf("ls-remote main: %v", err)
	}
	if len(hash) != 40 {
		t.Fatalf("hash = %q, want 40 hex chars", hash)
	}

	if _, err := gitutils.LsRemote(ctx, "file://"+upstream, "no-such-ref", gitutils.NoAuth); err == nil {
		t.Fatalf("expected error for missing ref")
	}

	if _, err := gitutils.LsRemote(ctx, "file://"+upstream, "", gitutils.NoAuth); err != nil {
		t.Fatalf("ls-remote default: %v", err)
	}
}

func newFixtureRepo(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: "refs/heads/main"},
		Bare:        false,
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("README"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{Author: &object.Signature{
		Name: "Tester", Email: "tester@example.com", When: time.Now(),
	}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func addCommit(t *testing.T, dir, file, contents, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(file); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{
		Name: "Tester", Email: "tester@example.com", When: time.Now(),
	}}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
