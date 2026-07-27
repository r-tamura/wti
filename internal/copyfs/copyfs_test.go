package copyfs

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCopy_File(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "out", "dst.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", info.Mode().Perm())
	}
}

func TestCopy_FileOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old-and-longer"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Errorf("content = %q, want %q (overwrite, no leftover bytes)", got, "new")
	}
}

func TestCopy_DirRecursive(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustMkdir(t, filepath.Join(src, "sub"))
	mustWrite(t, filepath.Join(src, "a.txt"), "a")
	mustWrite(t, filepath.Join(src, "sub", "b.txt"), "b")

	dst := filepath.Join(dir, "dst")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	if got := mustRead(t, filepath.Join(dst, "a.txt")); got != "a" {
		t.Errorf("a.txt = %q", got)
	}
	if got := mustRead(t, filepath.Join(dst, "sub", "b.txt")); got != "b" {
		t.Errorf("sub/b.txt = %q", got)
	}
}

// A tree with enough files to exercise the concurrent copy path: every file must
// arrive with its own content and mode, and nothing may be dropped.
func TestCopy_DirManyFilesConcurrent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	want := map[string]string{}
	for i := range 60 {
		rel := filepath.Join(fmt.Sprintf("sub%d", i%7), fmt.Sprintf("f%d.txt", i))
		content := fmt.Sprintf("content-%d", i)
		mustWrite(t, filepath.Join(src, rel), content)
		want[rel] = content
	}
	// One executable file, to confirm modes survive the parallel path.
	mustWrite(t, filepath.Join(src, "run.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(src, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")
	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	for rel, content := range want {
		if got := mustRead(t, filepath.Join(dst, rel)); got != content {
			t.Errorf("%s = %q, want %q", rel, got, content)
		}
	}
	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("run.sh mode = %v, want 0755", info.Mode().Perm())
	}
}

// A failure in one of the concurrent copies must surface from Copy rather than
// being swallowed by the worker that hit it.
func TestCopy_DirReportsFileError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	for _, name := range []string{"a.txt", "b.txt", "blocked.txt", "c.txt"} {
		mustWrite(t, filepath.Join(src, name), name)
	}

	// A directory where blocked.txt's copy wants to create a file.
	dst := filepath.Join(dir, "dst")
	mustMkdir(t, filepath.Join(dst, "blocked.txt"))

	if err := Copy(src, dst); err == nil {
		t.Fatal("Copy = nil, want an error for the undeletable destination")
	}
}

func TestCopy_SymlinkRecreated(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "link")
	if err := os.Symlink("./.env.local", src); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "link")

	if err := Copy(src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	target, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("readlink dst: %v", err)
	}
	if target != "./.env.local" {
		t.Errorf("link target = %q, want %q (not dereferenced)", target, "./.env.local")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
