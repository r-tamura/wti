package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	bi := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.2.3"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
			{Key: "vcs.time", Value: "2026-07-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}

	t.Run("ldflags win over build info", func(t *testing.T) {
		v, c, d := resolveVersion("v0.9.0", "abc123", "2026-06-30", bi, true)
		if v != "v0.9.0" || c != "abc123" || d != "2026-06-30" {
			t.Errorf("got %q/%q/%q, want ldflags values untouched", v, c, d)
		}
	})

	t.Run("go install: fall back to module version", func(t *testing.T) {
		v, c, _ := resolveVersion("dev", "none", "unknown", bi, true)
		if v != "v1.2.3" {
			t.Errorf("version = %q, want v1.2.3 from build info", v)
		}
		if c != "0123456789ab" {
			t.Errorf("commit = %q, want shortened revision", c)
		}
	})

	t.Run("go build in repo: derive from revision + dirty", func(t *testing.T) {
		devel := &debug.BuildInfo{
			Main: debug.Module{Version: "(devel)"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
				{Key: "vcs.modified", Value: "true"},
			},
		}
		v, _, _ := resolveVersion("dev", "none", "unknown", devel, true)
		if v != "0123456789ab-dirty" {
			t.Errorf("version = %q, want revision-derived dirty version", v)
		}
	})

	t.Run("no build info: keep defaults", func(t *testing.T) {
		v, _, _ := resolveVersion("dev", "none", "unknown", nil, false)
		if v != "dev" {
			t.Errorf("version = %q, want dev", v)
		}
	})
}

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

// Targets are copied concurrently, so each target's lines must stay contiguous
// in the output and appear in target order rather than interleaved.
func TestRun_ConcurrentTargetsKeepOutputGrouped(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "one.local\ntwo.local\n")
	write(t, filepath.Join(f.main, "one.local"), "1")
	write(t, filepath.Join(f.main, "two.local"), "2")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Verbose: 1}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}

	// Every line names its destination worktree; grouped output means the
	// destinations form runs, not an alternating sequence.
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		switch {
		case strings.Contains(line, f.featA):
			order = append(order, "a")
		case strings.Contains(line, f.featB):
			order = append(order, "b")
		default:
			t.Fatalf("unexpected output line: %q", line)
		}
	}
	if len(order) != 4 {
		t.Fatalf("got %d copy lines, want 4: %q", len(order), out.String())
	}
	if order[0] != order[1] || order[2] != order[3] || order[0] == order[2] {
		t.Errorf("output not grouped by target: %v\n%s", order, out.String())
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

func TestRun_DefaultOutputIsSummaryOnly(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\nextra.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")
	write(t, filepath.Join(f.main, "extra.local"), "extra")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "config.local") {
		t.Errorf("default output should not list individual files, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "copied 2 file(s)") {
		t.Errorf("default output should summarize the copy count, got: %q", out.String())
	}
}

func TestRun_SummaryCountsFilesInsideDirectories(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), ".claude\nconfig.local\n")
	write(t, filepath.Join(f.main, ".claude", "settings.json"), "{}")
	write(t, filepath.Join(f.main, ".claude", "agents", "a.md"), "a")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "copied 3 file(s)") {
		t.Errorf("summary should count files inside directories, got: %q", out.String())
	}
}

func TestRun_VerboseListsEachFile(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a", Verbose: 1}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "copied config.local -> ") {
		t.Errorf("-v should list each copied file, got: %q", out.String())
	}
}

func TestRun_VeryVerboseShowsResolutionDetails(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a", Verbose: 2}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "manifest: ") {
		t.Errorf("-vv should show the manifest path, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "target: ") {
		t.Errorf("-vv should show resolved targets, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "copied config.local -> ") {
		t.Errorf("-vv should still list each copied file, got: %q", out.String())
	}
}

func TestMain_ParsesVerbosityFlags(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "secret")
	t.Chdir(f.main)

	var out, errb bytes.Buffer
	if code := Main([]string{"-v", "feat-a"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "copied config.local ->") {
		t.Errorf("-v via Main should list files, got: %q", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := Main([]string{"-vv", "feat-a"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "manifest: ") {
		t.Errorf("-vv via Main should show resolution details, got: %q", out.String())
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

func TestRun_MissingLiteralIsSkippedSilentlyWithExitZero(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "present.txt\ntypo-missing.txt\n")
	write(t, filepath.Join(f.main, "present.txt"), "ok")

	var out, errb bytes.Buffer
	if code := run(f.main, Config{}, &out, &errb); code != 0 {
		t.Errorf("missing literal source should not cause non-zero exit, got %d", code)
	}
	if strings.Contains(errb.String(), "typo-missing.txt") {
		t.Errorf("warning should be hidden without --debug, got: %q", errb.String())
	}

	errb.Reset()
	run(f.main, Config{Debug: true}, &out, &errb)
	if !strings.Contains(errb.String(), "typo-missing.txt") {
		t.Errorf("--debug should warn about the missing file, got: %q", errb.String())
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

func TestRun_LiteralNegatedByPatternNotCopied(t *testing.T) {
	f := newFixture(t)
	// A literal include that a later "!" line re-excludes must NOT be copied,
	// even though the literal fast-path would otherwise stat+copy it directly.
	write(t, filepath.Join(f.main, ".worktreeinclude"), "config.local\nkeep.local\n!config.local\n")
	write(t, filepath.Join(f.main, "config.local"), "excluded")
	write(t, filepath.Join(f.main, "keep.local"), "kept")

	var out, errb bytes.Buffer
	code := run(f.main, Config{Target: "feat-a"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	if exists(filepath.Join(f.featA, "config.local")) {
		t.Error("config.local was re-excluded by !config.local and must NOT be copied")
	}
	if !exists(filepath.Join(f.featA, "keep.local")) {
		t.Error("keep.local should still be copied")
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

func TestRun_DeletesFilesRemovedFromSourceDirectory(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), ".claude\n")
	write(t, filepath.Join(f.main, ".claude", "keep.md"), "k")
	write(t, filepath.Join(f.featA, ".claude", "keep.md"), "old")
	write(t, filepath.Join(f.featA, ".claude", "gone.md"), "g")

	var out, errb bytes.Buffer
	if code := run(f.main, Config{Target: "feat-a"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errb.String())
	}
	if exists(filepath.Join(f.featA, ".claude", "gone.md")) {
		t.Error("gone.md should be deleted from the destination")
	}
	if got := read(t, filepath.Join(f.featA, ".claude", "keep.md")); got != "k" {
		t.Errorf("keep.md = %q", got)
	}
	if !strings.Contains(out.String(), "deleted 1 path(s)") {
		t.Errorf("summary should report deletions, got: %q", out.String())
	}
}

func TestRun_NoDeleteKeepsExtraneousFiles(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), ".claude\n")
	write(t, filepath.Join(f.main, ".claude", "keep.md"), "k")
	write(t, filepath.Join(f.featA, ".claude", "gone.md"), "g")

	var out, errb bytes.Buffer
	if code := run(f.main, Config{Target: "feat-a", NoDelete: true}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errb.String())
	}
	if !exists(filepath.Join(f.featA, ".claude", "gone.md")) {
		t.Error("--no-delete must keep gone.md")
	}
}

func TestRun_DryRunReportsDeletionsWithoutDeleting(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), ".claude\n")
	write(t, filepath.Join(f.main, ".claude", "keep.md"), "k")
	write(t, filepath.Join(f.featA, ".claude", "gone.md"), "g")

	var out, errb bytes.Buffer
	if code := run(f.main, Config{Target: "feat-a", DryRun: true}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errb.String())
	}
	if !exists(filepath.Join(f.featA, ".claude", "gone.md")) {
		t.Error("dry run must not delete")
	}
	if !strings.Contains(out.String(), "would delete .claude/gone.md") {
		t.Errorf("dry run should list deletions, got: %q", out.String())
	}
}

func TestRun_WildcardMatchedFilesAreNotDeleted(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.main, ".worktreeinclude"), "*.local\n")
	write(t, filepath.Join(f.main, "a.local"), "a")
	write(t, filepath.Join(f.featA, "b.local"), "b")

	var out, errb bytes.Buffer
	if code := run(f.main, Config{Target: "feat-a"}, &out, &errb); code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errb.String())
	}
	if !exists(filepath.Join(f.featA, "b.local")) {
		t.Error("files not under a mirrored directory must not be deleted")
	}
}
