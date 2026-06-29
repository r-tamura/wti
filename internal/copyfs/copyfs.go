// Package copyfs copies files, directories, and symlinks on the local
// filesystem, preserving file modes and recreating symlinks (rather than
// dereferencing them). Existing destination files are always overwritten.
package copyfs

import (
	"io"
	"os"
	"path/filepath"
)

// Copy copies the filesystem object at src to dst.
//
//   - Regular files are copied byte-for-byte, preserving their permission bits.
//     An existing dst is truncated and overwritten.
//   - Directories are copied recursively.
//   - Symlinks are recreated pointing at the same (possibly relative) target;
//     they are not dereferenced.
//
// Parent directories of dst are created as needed.
func Copy(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return copySymlink(src, dst)
	case info.IsDir():
		return copyDir(src, dst)
	default:
		return copyFile(src, dst, info.Mode().Perm())
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

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
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
			return copySymlink(path, target)
		case d.IsDir():
			return os.MkdirAll(target, dirPerm(info.Mode().Perm()))
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

// dirPerm guarantees the owner can traverse/write the copied directory.
func dirPerm(p os.FileMode) os.FileMode {
	return p | 0o700
}
