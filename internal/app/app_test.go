package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	main  string // main worktree path
	featA string // linked worktree "a"
	featB string // linked worktree "b"
}

// newFixture builds a main worktree with two linked worktrees.
func newFixture(t *testing.T) fixture {
	t.Helper()
	main := t.TempDir()
	git(t, main, "init", "-q", "-b", "main")
	git(t, main, "config", "user.email", "t@e.com")
	git(t, main, "config", "user.name", "t")
	write(t, filepath.Join(main, "README.md"), "x")
	git(t, main, "add", ".")
	git(t, main, "commit", "-q", "-m", "init")

	a := filepath.Join(t.TempDir(), "feat-a")
	b := filepath.Join(t.TempDir(), "feat-b")
	git(t, main, "worktree", "add", "-q", "-b", "a", a)
	git(t, main, "worktree", "add", "-q", "-b", "b", b)
	return fixture{main: main, featA: a, featB: b}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %q: %v", p, err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestRun_CopiesToAllLinkedWorktrees(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	var out, errb bytes.Buffer
	code := run(f.main, Config{}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if got := read(t, filepath.Join(f.featA, "config.local")); got != "secret" {
		t.Errorf("feat-a config.local = %q", got)
	}
	if got := read(t, filepath.Join(f.featB, "config.local")); got != "secret" {
		t.Errorf("feat-b config.local = %q", got)
	}
}

func TestRun_SingleTarget(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !exists(filepath.Join(f.featA, "config.local")) {
		t.Error("feat-a should have received the file")
	}
	if exists(filepath.Join(f.featB, "config.local")) {
		t.Error("feat-b should NOT have received the file (single target)")
	}
}

func TestRun_FromInsideLinkedWorktree(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	// Invoked from inside feat-a; source of truth is still the main worktree.
	var out, errb bytes.Buffer
	code := run(f.featA, Config{Target: "feat-b"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if got := read(t, filepath.Join(f.featB, "config.local")); got != "secret" {
		t.Errorf("feat-b config.local = %q", got)
	}
}

func TestRun_DryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	var out, errb bytes.Buffer
	code := run(f.main, Config{DryRun: true}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if exists(filepath.Join(f.featA, "config.local")) {
		t.Error("dry-run must not write to feat-a")
	}
	if !strings.Contains(out.String(), "config.local") {
		t.Errorf("dry-run should list planned copy, got: %q", out.String())
	}
}

func TestRun_MissingManifestErrors(t *testing.T) {
	f := newFixture(t)
	var out, errb bytes.Buffer
	code := run(f.main, Config{}, &out, &errb)
	if code == 0 {
		t.Error("missing .worktreeinclude should exit non-zero")
	}
}

func TestRun_MissingLiteralWarnsAndExitsNonZero(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "present.txt\ntypo-missing.txt\n")
	write(t, filepath.Join(f.main, "present.txt"), "ok")

	var out, errb bytes.Buffer
	code := run(f.main, Config{}, &out, &errb)
	if code == 0 {
		t.Error("missing literal source should cause non-zero exit")
	}
	if !strings.Contains(errb.String(), "typo-missing.txt") {
		t.Errorf("stderr should warn about the missing file, got: %q", errb.String())
	}
	// The present file must still be copied despite the missing one.
	if !exists(filepath.Join(f.featA, "present.txt")) {
		t.Error("present.txt should still be copied")
	}
}

func TestRun_WildcardMatching(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "*.env\n!secret.env\n")
	write(t, filepath.Join(f.main, "local.env"), "L")
	write(t, filepath.Join(f.main, "secret.env"), "S")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !exists(filepath.Join(f.featA, "local.env")) {
		t.Error("local.env should be copied by *.env")
	}
	if exists(filepath.Join(f.featA, "secret.env")) {
		t.Error("secret.env should be re-excluded by !secret.env")
	}
}

func TestRun_WalkSkipsNestedWorktree(t *testing.T) {
	f := newFixture(t)
	// Create a worktree nested under the main worktree's tracked dir.
	nested := filepath.Join(f.main, ".claude", "worktrees", "wt1")
	git(t, f.main, "worktree", "add", "-q", "-b", "nested", nested)
	write(t, filepath.Join(f.main, ".worktreeinclude"), ".claude/**\n")
	write(t, filepath.Join(f.main, ".claude", "settings.json"), "{}")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !exists(filepath.Join(f.featA, ".claude", "settings.json")) {
		t.Error(".claude/settings.json should be copied")
	}
	// The nested worktree's contents must not be pulled in.
	if exists(filepath.Join(f.featA, ".claude", "worktrees", "wt1", "README.md")) {
		t.Error("nested worktree contents must be skipped during walk")
	}
}
