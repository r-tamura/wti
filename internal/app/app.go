// Package app wires the manifest, worktree, and copyfs packages into the wti
// command's behavior.
package app

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/r-tamura/wti/internal/copyfs"
	"github.com/r-tamura/wti/internal/manifest"
	"github.com/r-tamura/wti/internal/worktree"
)

const manifestName = ".worktreeinclude"

var buildVersion, buildCommit, buildDate = "dev", "none", "unknown"

// SetVersion records build information (typically injected via -ldflags) for
// the --version flag.
func SetVersion(version, commit, date string) {
	buildVersion, buildCommit, buildDate = version, commit, date
}

// versionString returns the effective version line, falling back to the Go
// build info embedded by `go install`/`go build` when -ldflags were not used.
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	v, c, d := resolveVersion(buildVersion, buildCommit, buildDate, bi, ok)
	return fmt.Sprintf("wti %s (commit %s, built %s)", v, c, d)
}

// resolveVersion decides the version/commit/date to report. Values injected via
// -ldflags take precedence; otherwise it derives them from the module version
// and VCS stamps recorded in the build info.
func resolveVersion(ldVersion, ldCommit, ldDate string, bi *debug.BuildInfo, ok bool) (version, commit, date string) {
	version, commit, date = ldVersion, ldCommit, ldDate
	if version != "dev" || !ok || bi == nil {
		return version, commit, date
	}

	// A real (or pseudo) module version already encodes dirtiness; only a
	// version we derive ourselves from the raw revision needs a -dirty suffix.
	derived := false
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		version = v
	}

	var rev, modified string
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			date = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev != "" {
		short := rev
		if len(short) > 12 {
			short = short[:12]
		}
		commit = short
		if version == "dev" {
			version = short
			derived = true
		}
	}
	if derived && modified == "true" {
		version += "-dirty"
	}
	return version, commit, date
}

// Config holds the resolved command-line options.
type Config struct {
	DryRun   bool
	NoDelete bool // keep destination files that no longer exist in the source directory
	Debug    bool
	Verbose  int    // 0 = summary only, 1 (-v) = per-file, 2 (-vv) = resolution details
	Target   string // positional worktree selector; empty = all linked worktrees
}

const usage = `wti — copy files listed in .worktreeinclude into git worktrees

Usage:
  wti [flags] [worktree]

Arguments:
  worktree   Copy only into the worktree matching this path fragment or branch
             name. Omit to copy into every linked worktree.

Flags:
`

// Main parses args and runs the command, returning a process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wti", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	var cfg Config
	var showVersion, verbose, veryVerbose bool
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "show planned actions without writing anything")
	fs.BoolVar(&cfg.NoDelete, "no-delete", false, "keep destination files that no longer exist in a copied source directory")
	fs.BoolVar(&cfg.Debug, "debug", false, "print full error details and warnings")
	fs.BoolVar(&verbose, "v", false, "list each copied file")
	fs.BoolVar(&veryVerbose, "vv", false, "also show manifest and target resolution details")
	fs.BoolVar(&showVersion, "version", false, "print version information and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if showVersion {
		fmt.Fprintln(stdout, versionString())
		return 0
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "wti: at most one worktree argument is allowed")
		return 2
	}
	cfg.Target = fs.Arg(0)
	if veryVerbose {
		cfg.Verbose = 2
	} else if verbose {
		cfg.Verbose = 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "wti: %v\n", err)
		return 1
	}
	return run(cwd, cfg, stdout, stderr)
}

// run executes the command from cwd and returns an exit code.
func run(cwd string, cfg Config, stdout, stderr io.Writer) int {
	main, err := worktree.Main(cwd)
	if err != nil {
		return fail(stderr, cfg, err)
	}

	manifestPath := filepath.Join(main.Path, manifestName)
	if cfg.Verbose >= 2 {
		fmt.Fprintf(stdout, "manifest: %s\n", manifestPath)
	}
	m, err := manifest.ParseFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fail(stderr, cfg, fmt.Errorf("%s not found in main worktree %s", manifestName, main.Path))
		}
		return fail(stderr, cfg, err)
	}

	targets, err := resolveTargets(cwd, cfg.Target)
	if err != nil {
		return fail(stderr, cfg, err)
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "wti: no linked worktrees to copy into")
		return 0
	}

	paths, missing, err := collectSources(main.Path, m)
	if err != nil {
		return fail(stderr, cfg, err)
	}

	// A literal line whose source does not exist is simply skipped; it is not
	// an error. The warning is only shown with --debug.
	for _, name := range missing {
		if cfg.Debug {
			fmt.Fprintf(stderr, "wti: warning: source not found, skipping: %s\n", name)
		}
	}

	exitCode := 0

	results := copyToTargets(main.Path, targets, paths, cfg)
	for i := range results {
		fmt.Fprint(stdout, results[i].stdout.String())
		fmt.Fprint(stderr, results[i].stderr.String())
		if results[i].failed {
			exitCode = 1
		}
	}

	return exitCode
}

// targetResult holds one target's buffered output and whether any of its copies
// failed.
type targetResult struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
	failed bool
}

// copyToTargets copies paths into every target concurrently, one goroutine per
// target. Targets are distinct worktrees, so the copies themselves never touch
// the same file; the only shared resource is the output, which each worker
// buffers so lines from different targets cannot interleave. Results are
// returned in target order, keeping the printed output deterministic.
func copyToTargets(mainPath string, targets []worktree.Worktree, paths []string, cfg Config) []targetResult {
	results := make([]targetResult, len(targets))
	var wg sync.WaitGroup
	for i, tgt := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copyToTarget(mainPath, tgt, paths, cfg, &results[i])
		}()
	}
	wg.Wait()
	return results
}

// copyToTarget copies every path from mainPath into tgt, recording progress and
// failures in res.
func copyToTarget(mainPath string, tgt worktree.Worktree, paths []string, cfg Config, res *targetResult) {
	if cfg.Verbose >= 2 {
		fmt.Fprintf(&res.stdout, "target: %s\n", tgt.Path)
	}
	copied, deleted := 0, 0
	for _, rel := range paths {
		src := filepath.Join(mainPath, rel)
		dst := filepath.Join(tgt.Path, rel)
		if cfg.DryRun {
			fmt.Fprintf(&res.stdout, "would copy %s -> %s\n", rel, tgt.Path)
		} else {
			n, err := copyfs.Copy(src, dst)
			if err != nil {
				fmt.Fprintf(&res.stderr, "wti: copy %s -> %s: %v\n", rel, tgt.Path, err)
				res.failed = true
				continue
			}
			copied += n
			if cfg.Verbose >= 1 {
				fmt.Fprintf(&res.stdout, "copied %s -> %s\n", rel, tgt.Path)
			}
		}
		if cfg.NoDelete {
			continue
		}
		removed, err := copyfs.Prune(src, dst, cfg.DryRun)
		if err != nil {
			fmt.Fprintf(&res.stderr, "wti: delete in %s -> %s: %v\n", rel, tgt.Path, err)
			res.failed = true
		}
		deleted += len(removed)
		for _, r := range removed {
			if cfg.DryRun {
				fmt.Fprintf(&res.stdout, "would delete %s -> %s\n", path.Join(rel, r), tgt.Path)
			} else if cfg.Verbose >= 1 {
				fmt.Fprintf(&res.stdout, "deleted %s -> %s\n", path.Join(rel, r), tgt.Path)
			}
		}
	}
	if !cfg.DryRun && cfg.Verbose == 0 {
		fmt.Fprintf(&res.stdout, "copied %d file(s) -> %s\n", copied, tgt.Path)
		if deleted > 0 {
			fmt.Fprintf(&res.stdout, "deleted %d path(s) -> %s\n", deleted, tgt.Path)
		}
	}
}

func resolveTargets(cwd, target string) ([]worktree.Worktree, error) {
	linked, err := worktree.Linked(cwd)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return linked, nil
	}
	w, err := worktree.Resolve(linked, target)
	if err != nil {
		return nil, err
	}
	return []worktree.Worktree{w}, nil
}

// collectSources resolves the relative paths to copy from mainPath. It returns
// the selected paths and any literal paths whose source was missing.
func collectSources(mainPath string, m *manifest.Manifest) (paths []string, missing []string, err error) {
	seen := map[string]bool{}
	add := func(rel string) {
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			seen[rel] = true
			paths = append(paths, rel)
		}
	}

	// Literal lines are resolved by direct stat, both to surface missing
	// sources (typos) and, in the common no-pattern case, to copy without
	// walking the tree. When the manifest also contains wildcard/negation
	// lines, a literal may be re-excluded by a later "!" pattern, so its
	// inclusion is decided by the walk below (which applies the full matcher)
	// rather than added here unconditionally.
	for _, lit := range m.Literals() {
		rel := strings.TrimSuffix(lit, "/")
		if _, err := os.Lstat(filepath.Join(mainPath, filepath.FromSlash(rel))); err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, rel)
				continue
			}
			return nil, nil, err
		}
		if !m.HasPatterns() {
			add(rel)
		}
	}

	// Slow path: walk the tree for wildcard / negation lines.
	if m.HasPatterns() {
		skip, err := nestedWorktreeDirs(mainPath)
		if err != nil {
			return nil, nil, err
		}
		walkErr := filepath.WalkDir(mainPath, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == mainPath {
				return nil
			}
			rel, err := filepath.Rel(mainPath, p)
			if err != nil {
				return err
			}
			relSlash := filepath.ToSlash(rel)

			if d.IsDir() {
				if base := filepath.Base(p); base == ".git" {
					return filepath.SkipDir
				}
				if skip[p] {
					return filepath.SkipDir
				}
				return nil
			}
			if m.Match(relSlash) {
				add(relSlash)
			}
			return nil
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}

	sort.Strings(paths)
	return paths, missing, nil
}

// nestedWorktreeDirs returns the set of worktree paths that live underneath
// mainPath (so the tree walk can skip them).
func nestedWorktreeDirs(mainPath string) (map[string]bool, error) {
	all, err := worktree.List(mainPath)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, w := range all {
		if w.Path == mainPath {
			continue
		}
		rel, err := filepath.Rel(mainPath, w.Path)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			set[w.Path] = true
		}
	}
	return set, nil
}

func fail(stderr io.Writer, cfg Config, err error) int {
	if cfg.Debug {
		fmt.Fprintf(stderr, "wti: %+v\n", err)
	} else {
		fmt.Fprintf(stderr, "wti: %v\n", err)
	}
	return 1
}
