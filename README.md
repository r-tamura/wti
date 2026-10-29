# wti

`wti` (*worktree-include*) copies the files listed in a repository's
`.worktreeinclude` manifest into git worktrees.

These are usually files that are **locally git-excluded** — env files, editor
config, local notes — so they never appear in a freshly created worktree. `wti`
copies them explicitly so a new worktree is immediately usable.

## Install

```sh
go install github.com/r-tamura/wti@latest
```

## Usage

```sh
# Copy into every linked worktree (except the main one)
wti

# Copy into a single worktree (matched by path fragment or branch name)
wti my-feature

# Preview without writing anything
wti --dry-run

# Keep destination files that were deleted from the source directory
wti --no-delete

# List each copied file (default output is a per-worktree summary)
wti -v

# Also show manifest and target resolution details
wti -vv
```

`wti` always reads `.worktreeinclude` from the **main worktree**, regardless of
the directory you run it from.

## Deleted files

Like `rsync --delete`, a copied **directory** is mirrored: files and
subdirectories that exist in the worktree but no longer exist in the main
worktree's directory are removed. Pass `--no-delete` to keep them, and
`--dry-run` to see what would be removed (`would delete ...`).

Only directories listed literally in the manifest (e.g. `docs/notes/`) are
mirrored. Individual files — including anything matched by a wildcard or `!`
pattern — are only overwritten; a file deleted from the main worktree is not
removed from the others.

## `.worktreeinclude`

One pattern per line, using `.gitignore`-style syntax (`!` negation, `**`,
anchoring). Lines starting with `#` and blank lines are ignored. A path that
matches is **included** (copied); `!` re-excludes.

```
# editor config
pyrightconfig.json

# local context
CONTEXT.md
docs/notes/

# env
.env.local
```

## License

[0BSD](./LICENSE)
