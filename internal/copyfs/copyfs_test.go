package copyfs

import (
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
