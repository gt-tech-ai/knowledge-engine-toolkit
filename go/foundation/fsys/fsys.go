// Package fsys provides a real-OS filesystem implementation of the FS
// interface defined in core/interfaces.
package fsys

import (
	"io/fs"
	"os"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// FS is a type alias for the core FS interface, allowing callers to reference
// fsys.FS without importing core/interfaces directly.
type FS = interfaces.FS

// Compile-time check that OSFS implements FS interface.
var _ interfaces.FS = (*OSFS)(nil)

// OSFS is the default filesystem implementation that delegates to the os package.
// It is safe for concurrent use by multiple goroutines.
type OSFS struct{}

// NewOSFS creates a new OSFS instance.
func NewOSFS() *OSFS {
	return &OSFS{}
}

// DefaultFS returns the default filesystem implementation.
func DefaultFS() interfaces.FS {
	return NewOSFS()
}

// Stat returns file information, delegating to os.Stat.
func (f *OSFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

// MkdirAll creates a directory tree, delegating to os.MkdirAll.
func (f *OSFS) MkdirAll(
	path string,
	perm fs.FileMode,
) error {
	return os.MkdirAll(path, perm)
}

// ReadFile reads an entire file, delegating to os.ReadFile.
func (f *OSFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// WriteFile writes data to a file, delegating to os.WriteFile.
func (f *OSFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// RemoveAll removes a path and its children, delegating to os.RemoveAll.
func (f *OSFS) RemoveAll(path string) error { return os.RemoveAll(path) }

// Rename renames a file or directory, delegating to os.Rename.
func (f *OSFS) Rename(
	oldpath, newpath string,
) error {
	return os.Rename(oldpath, newpath)
}

// ReadDir reads a directory's entries, delegating to os.ReadDir.
func (f *OSFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
