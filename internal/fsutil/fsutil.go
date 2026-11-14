// Package fsutil holds the file operations every other package relies on: atomic
// writes, locks, tree hashing, verified copies and moves that never follow symlinks.
package fsutil

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// WriteFileAtomic writes data to path through a temp file in the same folder plus rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Lock is an exclusive advisory lock on a file.
type Lock struct{ f *os.File }

// LockFile takes an exclusive flock on path, creating it. It blocks.
func LockFile(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err == syscall.EINTR {
			continue
		}
		break
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock.
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// Exists reports whether path exists without following a final symlink.
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// IsSymlink reports whether path is a symlink.
func IsSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// LinkTarget returns the target of the symlink at path, or "".
func LinkTarget(path string) string {
	t, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return t
}

// HashBytes returns the hex SHA-256 of b.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// HashFile hashes a regular file's content.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashTree hashes path recursively: names, kinds, file contents and link targets. Symlinks
// are hashed as links and never followed. A missing path hashes to "".
func HashTree(path string) (string, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if err := hashInto(h, path, fi); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashInto(h io.Writer, path string, fi fs.FileInfo) error {
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "L%d:%s;", len(t), t)
	case fi.IsDir():
		ents, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(ents))
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		fmt.Fprintf(h, "D%d{", len(names))
		for _, n := range names {
			cfi, err := os.Lstat(filepath.Join(path, n))
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%d:%s=", len(n), n)
			if err := hashInto(h, filepath.Join(path, n), cfi); err != nil {
				return err
			}
		}
		fmt.Fprint(h, "}")
	case fi.Mode().IsRegular():
		s, err := HashFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "F%s;", s)
	default:
		fmt.Fprintf(h, "O%s;", fi.Mode().Type().String())
	}
	return nil
}

// TreeSize returns the total size in bytes of regular files under path (links not followed).
func TreeSize(path string) int64 {
	var n int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// CopyTree copies src to dst. dst must not exist. Symlinks are copied as links, never
// followed. Regular files are cloned when the filesystem supports it. The copy is verified
// by hash.
func CopyTree(src, dst string) error {
	if Exists(dst) {
		return fmt.Errorf("copy: %s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := copyAny(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	a, err := HashTree(src)
	if err != nil {
		return err
	}
	b, err := HashTree(dst)
	if err != nil {
		return err
	}
	if a != b {
		_ = os.RemoveAll(dst)
		return fmt.Errorf("copy of %s did not verify", src)
	}
	return nil
}

func copyAny(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(t, dst)
	case fi.IsDir():
		if err := os.Mkdir(dst, fi.Mode().Perm()|0o700); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyAny(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, fi.Mode().Perm())
	case fi.Mode().IsRegular():
		if cloneFile(src, dst) == nil {
			return nil
		}
		return copyFile(src, dst, fi.Mode().Perm())
	default:
		return fmt.Errorf("copy: %s is not a file, folder or link", src)
	}
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// MoveTree moves src to dst. dst must not exist. A rename is tried first; across file
// systems it copies, verifies by hash, and only then removes the source (links are removed
// as links, never followed).
func MoveTree(src, dst string) error {
	if Exists(dst) {
		return fmt.Errorf("move: %s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) || !errors.Is(le.Err, syscall.EXDEV) {
		return err
	}
	if err := CopyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// RemoveTree removes path without following symlinks (os.RemoveAll never follows them).
func RemoveTree(path string) error { return os.RemoveAll(path) }

// WriteThrough writes data atomically to path; when path is a symlink it writes to the
// link's final target instead, so the link (for example into a dotfiles folder) survives.
func WriteThrough(path string, data []byte, perm os.FileMode) error {
	if IsSymlink(path) {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%s is a link whose target cannot be resolved: %v", path, err)
		}
		path = target
	}
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return WriteFileAtomic(path, data, perm)
}
