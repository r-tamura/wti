// Package manifest parses a .worktreeinclude file and decides which paths it
// selects for copying.
//
// The syntax is .gitignore-style (supporting "!" negation, "**", and
// anchoring) but the semantics are inverted: a path that *matches* is
// **included** (copied), and a "!" line *re-excludes* a previously included
// path.
package manifest

import (
	"bufio"
	"io"
	"os"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// Manifest is a parsed .worktreeinclude.
type Manifest struct {
	matcher     *ignore.GitIgnore
	literals    []string
	hasPatterns bool
}

// Parse reads a manifest from r.
func Parse(r io.Reader) (*Manifest, error) {
	var lines []string
	var literals []string
	hasPatterns := false

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
		if isLiteral(line) {
			literals = append(literals, line)
		} else {
			hasPatterns = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	return &Manifest{
		matcher:     ignore.CompileIgnoreLines(lines...),
		literals:    literals,
		hasPatterns: hasPatterns,
	}, nil
}

// ParseFile reads and parses the manifest at path.
func ParseFile(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Match reports whether relPath (a slash-separated path relative to the main
// worktree root) is selected for copying.
func (m *Manifest) Match(relPath string) bool {
	return m.matcher.MatchesPath(relPath)
}

// Literals returns the literal include lines (no wildcards, no negation), in
// file order. These can be copied directly without walking the tree.
func (m *Manifest) Literals() []string {
	return m.literals
}

// HasPatterns reports whether the manifest contains any wildcard or negation
// line, which requires walking the tree to evaluate.
func (m *Manifest) HasPatterns() bool {
	return m.hasPatterns
}

// isLiteral reports whether a manifest line is a plain path with no wildcard
// and no negation, so it can be resolved with a direct stat.
func isLiteral(line string) bool {
	if strings.HasPrefix(line, "!") {
		return false
	}
	return !strings.ContainsAny(line, "*?[")
}
