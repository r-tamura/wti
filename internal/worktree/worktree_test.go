package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo creates a git repo with one commit and returns the main worktree path.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func addWorktree(t *testing.T, repo, path, branch string) {
	t.Helper()
	runGit(t, repo, "worktree", "add", "-q", "-b", branch, path)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestMain_FromMainWorktree(t *testing.T) {
	repo := newRepo(t)
	main, err := Main(repo)
	if err != nil {
		t.Fatalf("Main: %v", err)
	}
	if !samePath(t, main.Path, repo) {
		t.Errorf("Main().Path = %q, want %q", main.Path, repo)
	}
}

func TestMain_FromLinkedWorktree(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(t.TempDir(), "feature")
	addWorktree(t, repo, wt, "feature")

	// Even when invoked from inside the linked worktree, Main resolves the
	// original (main) worktree.
	main, err := Main(wt)
	if err != nil {
		t.Fatalf("Main: %v", err)
	}
	if !samePath(t, main.Path, repo) {
		t.Errorf("Main().Path = %q, want %q (resolved from linked worktree)", main.Path, repo)
	}
}

func TestLinked_ExcludesMain(t *testing.T) {
	repo := newRepo(t)
	wtA := filepath.Join(t.TempDir(), "a")
	wtB := filepath.Join(t.TempDir(), "b")
	addWorktree(t, repo, wtA, "a")
	addWorktree(t, repo, wtB, "b")

	linked, err := Linked(repo)
	if err != nil {
		t.Fatalf("Linked: %v", err)
	}
	if len(linked) != 2 {
		t.Fatalf("len(Linked) = %d, want 2", len(linked))
	}
	for _, w := range linked {
		if samePath(t, w.Path, repo) {
			t.Errorf("Linked included the main worktree %q", repo)
		}
	}
}

func TestResolve_ByPathFragment(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(t.TempDir(), "my-feature")
	addWorktree(t, repo, wt, "feat-x")

	linked, _ := Linked(repo)
	got, err := Resolve(linked, "my-feature")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !samePath(t, got.Path, wt) {
		t.Errorf("Resolve path fragment = %q, want %q", got.Path, wt)
	}
}

func TestResolve_ByBranch(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wtdir")
	addWorktree(t, repo, wt, "feat-x")

	linked, _ := Linked(repo)
	got, err := Resolve(linked, "feat-x")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !samePath(t, got.Path, wt) {
		t.Errorf("Resolve by branch = %q, want %q", got.Path, wt)
	}
}

func TestResolve_Unknown(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wtdir")
	addWorktree(t, repo, wt, "feat-x")

	linked, _ := Linked(repo)
	if _, err := Resolve(linked, "nope"); err == nil {
		t.Error("Resolve(unknown) should return an error")
	}
}

func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatalf("evalsymlinks %q: %v", a, err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatalf("evalsymlinks %q: %v", b, err)
	}
	return ra == rb
}
