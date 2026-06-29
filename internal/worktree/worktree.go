// Package worktree enumerates and resolves git worktrees by shelling out to
// `git worktree list --porcelain`.
package worktree

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Worktree describes a single git worktree.
type Worktree struct {
	// Path is the absolute filesystem path of the worktree.
	Path string
	// Branch is the short branch name (e.g. "main"), or "" if detached.
	Branch string
}

// List returns all worktrees registered for the repository containing dir.
// The first element is always the main worktree.
func List(dir string) ([]Worktree, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git worktree list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parsePorcelain(stdout.Bytes()), nil
}

// Main returns the main worktree for the repository containing dir, regardless
// of whether dir is the main worktree or a linked one.
func Main(dir string) (Worktree, error) {
	wts, err := List(dir)
	if err != nil {
		return Worktree{}, err
	}
	if len(wts) == 0 {
		return Worktree{}, fmt.Errorf("no worktrees found for %q", dir)
	}
	return wts[0], nil
}

// Linked returns every worktree except the main one.
func Linked(dir string) ([]Worktree, error) {
	wts, err := List(dir)
	if err != nil {
		return nil, err
	}
	if len(wts) <= 1 {
		return nil, nil
	}
	return wts[1:], nil
}

// Resolve selects a single worktree from wts matching arg, where arg may be a
// branch name or a fragment of the worktree path. It is an error if zero or
// more than one worktree matches.
func Resolve(wts []Worktree, arg string) (Worktree, error) {
	var matches []Worktree
	for _, w := range wts {
		if w.Branch == arg || strings.Contains(w.Path, arg) {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Worktree{}, fmt.Errorf("no worktree matches %q", arg)
	default:
		paths := make([]string, len(matches))
		for i, w := range matches {
			paths[i] = w.Path
		}
		return Worktree{}, fmt.Errorf("%q is ambiguous, matches: %s", arg, strings.Join(paths, ", "))
	}
}

func parsePorcelain(data []byte) []Worktree {
	var wts []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			wts = append(wts, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case cur != nil && strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "":
			flush()
		}
	}
	flush()
	return wts
}
