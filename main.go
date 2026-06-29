// Command wti copies the files listed in a repository's .worktreeinclude
// manifest into git worktrees.
package main

import (
	"os"

	"github.com/r-tamura/wti/internal/app"
)

// Build information, set via -ldflags by goreleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	app.SetVersion(version, commit, date)
	os.Exit(app.Main(os.Args[1:], os.Stdout, os.Stderr))
}
