// Package app wires the manifest, worktree, and copyfs packages into the wti
// command's behavior.
package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/r-tamura/wti/internal/copyfs"
	"github.com/r-tamura/wti/internal/manifest"
	"github.com/r-tamura/wti/internal/worktree"
)

const manifestName = ".worktreeinclude"

var buildVersion, buildCommit, buildDate = "dev", "none", "unknown"

// SetVersion records build information for the --version flag.
func SetVersion(version, commit, date string) {
	buildVersion, buildCommit, buildDate = version, commit, date
}

// Config holds the resolved command-line options.
type Config struct {
	DryRun bool
	Debug  bool
	Target string // positional worktree selector; empty = all linked worktrees
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
	var showVersion bool
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "show planned actions without writing anything")
	fs.BoolVar(&cfg.Debug, "debug", false, "print full error details on failure")
	fs.BoolVar(&showVersion, "version", false, "print version information and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if showVersion {
		fmt.Fprintf(stdout, "wti %s (commit %s, built %s)\n", buildVersion, buildCommit, buildDate)
		return 0
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "wti: at most one worktree argument is allowed")
		return 2
	}
	cfg.Target = fs.Arg(0)

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

	exitCode := 0
	for _, name := range missing {
		fmt.Fprintf(stderr, "wti: warning: source not found, skipping: %s\n", name)
		exitCode = 1
	}

	for _, tgt := range targets {
		for _, rel := range paths {
			src := filepath.Join(main.Path, rel)
			dst := filepath.Join(tgt.Path, rel)
			if cfg.DryRun {
				fmt.Fprintf(stdout, "would copy %s -> %s\n", rel, tgt.Path)
				continue
			}
			if err := copyfs.Copy(src, dst); err != nil {
				fmt.Fprintf(stderr, "wti: copy %s -> %s: %v\n", rel, tgt.Path, err)
				exitCode = 1
				continue
			}
			fmt.Fprintf(stdout, "copied %s -> %s\n", rel, tgt.Path)
		}
	}

	return exitCode
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
