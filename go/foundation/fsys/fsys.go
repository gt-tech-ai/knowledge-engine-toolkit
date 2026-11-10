// Package fsys provides a real-OS filesystem implementation of the FS
// interface defined in core/interfaces.
package fsys

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// FS is a type alias for the core FS interface, allowing callers to reference
// fsys.FS without importing core/interfaces directly.
type FS = interfaces.FS

// Compile-time check that OSFS implements FS interface.
var _ interfaces.FS = (*OSFS)(nil)

// OSFS is the default filesystem implementation that delegates to the os package; every
// path it reads or writes by name goes through filepath.Clean first. It is safe for
// concurrent use by multiple goroutines.
type OSFS struct{}

// NewOSFS creates a new OSFS instance.
func NewOSFS() *OSFS {
	return &OSFS{}
}

// DefaultFS returns the default filesystem implementation.
func DefaultFS() interfaces.FS {
	return NewOSFS()
}

// wrap codes a file-system error for the caller: a missing path is CodeNotFound and
// anything else CodeInternal. The os error stays in the chain, so a caller tests it with
// errors.Is(err, fs.ErrNotExist) or errors.Is(err, fs.ErrExist); os.IsNotExist, which
// does not unwrap, no longer matches. A nil err stays nil.
func wrap(err error, operation, name string) error {
	if err == nil {
		return nil
	}

	code := apperr.CodeInternal
	if apperr.StdIs(err, fs.ErrNotExist) {
		code = apperr.CodeNotFound
	}

	return apperr.Wrap(err, code, operation+" "+name)
}

// Stat returns file information, delegating to os.Stat.
func (*OSFS) Stat(name string) (fs.FileInfo, error) {
	info, err := os.Stat(filepath.Clean(name))

	return info, wrap(err, "stat", name)
}

// MkdirAll creates a directory tree, delegating to os.MkdirAll.
func (*OSFS) MkdirAll(
	path string,
	perm fs.FileMode,
) error {
	return wrap(os.MkdirAll(path, perm), "create directory", path)
}

// ReadFile reads an entire file, delegating to os.ReadFile.
func (*OSFS) ReadFile(
	name string,
) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(name))

	return data, wrap(err, "read", name)
}

// WriteFile writes data to a file, delegating to os.WriteFile.
func (*OSFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return wrap(os.WriteFile(filepath.Clean(name), data, perm), "write", name)
}

// RemoveAll removes a path and its children, delegating to os.RemoveAll.
func (*OSFS) RemoveAll(path string) error {
	return wrap(os.RemoveAll(path), "remove", path)
}

// Rename renames a file or directory, delegating to os.Rename.
func (*OSFS) Rename(
	oldpath, newpath string,
) error {
	return wrap(os.Rename(oldpath, newpath), "rename", oldpath)
}

// ReadDir reads a directory's entries, delegating to os.ReadDir.
func (*OSFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(filepath.Clean(name))

	return entries, wrap(err, "read directory", name)
}
