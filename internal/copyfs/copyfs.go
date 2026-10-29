// Package copyfs copies files, directories, and symlinks on the local
// filesystem, preserving file modes and recreating symlinks (rather than
// dereferencing them). Existing destination files are always overwritten.
//
// Directory copies fan the individual file copies out over a bounded pool of
// goroutines; Copy itself is safe to call concurrently as long as the callers
// write to distinct destinations.
package copyfs

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Copy copies the filesystem object at src to dst and returns the number of
// files copied: regular files and symlinks count as one each, and a directory
// counts every file inside it, not the directory itself. The count is only
// meaningful when the returned error is nil.
//
//   - Regular files are copied byte-for-byte, preserving their permission bits.
//     An existing dst is truncated and overwritten.
//   - Directories are copied recursively.
//   - Symlinks are recreated pointing at the same (possibly relative) target;
//     they are not dereferenced.
//
// Parent directories of dst are created as needed.
func Copy(src, dst string) (int, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return 0, err
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		if err := copySymlink(src, dst); err != nil {
			return 0, err
		}
		return 1, nil
	case info.IsDir():
		return copyDir(src, dst)
	default:
		if err := copyFile(src, dst, info.Mode().Perm()); err != nil {
			return 0, err
		}
		return 1, nil
	}
}

func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Ensure the mode is applied even if dst pre-existed with a different one.
	return os.Chmod(dst, perm)
}

func copySymlink(src, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// Remove any existing destination so the symlink can be (re)created.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(target, dst)
}

// copyDir copies a tree in two passes: the walk creates the destination
// directories and symlinks (cheap, and a directory must exist before the files
// inside it), then the regular files are copied concurrently, which is where the
// time in a large tree actually goes.
func copyDir(src, dst string) (int, error) {
	files, symlinks, err := prepareTree(src, dst)
	if err != nil {
		return 0, err
	}
	if err := copyFiles(files); err != nil {
		return 0, err
	}
	return len(files) + symlinks, nil
}

// fileCopy is one pending regular-file copy.
type fileCopy struct {
	src, dst string
	perm     os.FileMode
}

// prepareTree walks src creating the directories and symlinks under dst, and
// returns the regular files still to be copied along with the number of
// symlinks it recreated.
func prepareTree(src, dst string) ([]fileCopy, int, error) {
	var files []fileCopy
	symlinks := 0
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if err := copySymlink(path, target); err != nil {
				return err
			}
			symlinks++
			return nil
		case d.IsDir():
			return os.MkdirAll(target, dirPerm(info.Mode().Perm()))
		default:
			files = append(files, fileCopy{src: path, dst: target, perm: info.Mode().Perm()})
			return nil
		}
	})
	if err != nil {
		return nil, 0, err
	}
	return files, symlinks, nil
}

// copyFiles copies files with at most maxConcurrentCopies in flight. It returns
// the first error and stops starting further copies once one occurs, so a failed
// tree copy leaves the same partial result as the sequential version did.
func copyFiles(files []fileCopy) error {
	// Small trees are not worth the goroutine setup.
	if len(files) < 2 {
		for _, f := range files {
			if err := copyFile(f.src, f.dst, f.perm); err != nil {
				return err
			}
		}
		return nil
	}

	sem := make(chan struct{}, maxConcurrentCopies())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, f := range files {
		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop {
			break
		}

		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := copyFile(f.src, f.dst, f.perm); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// maxConcurrentCopies bounds the in-flight copies of a single tree. Each copy
// holds two open file descriptors, so the cap keeps a large tree well clear of
// the process limit — including when callers copy into several destinations at
// once, which multiplies this by the number of destinations. Beyond a handful of
// workers the disk, not the CPU, is the limit.
func maxConcurrentCopies() int {
	return min(max(runtime.NumCPU(), 1), 8)
}

// dirPerm guarantees the owner can traverse/write the copied directory.
func dirPerm(p os.FileMode) os.FileMode {
	return p | 0o700
}

// Prune removes everything under the directory dst that has no counterpart in
// the directory src, so that after a Copy dst mirrors src (like rsync --delete).
// It returns the paths removed, relative to dst and slash-separated; a removed
// directory is reported once, not once per entry inside it. With dryRun nothing
// is removed and the result lists what would be.
//
// Prune does nothing unless both src and dst are directories. Symlinks are never
// followed: an extraneous symlink is removed itself, not its target.
func Prune(src, dst string, dryRun bool) ([]string, error) {
	if info, err := os.Lstat(src); err != nil || !info.IsDir() {
		return nil, err
	}
	if info, err := os.Lstat(dst); err != nil || !info.IsDir() {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var removed []string
	err := filepath.WalkDir(dst, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dst, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(src, rel)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if !dryRun {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
		removed = append(removed, filepath.ToSlash(rel))
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return removed, err
}
