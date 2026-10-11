package interfaces

import "io/fs"

// FS is a filesystem abstraction interface that wraps common os package
// operations. Implementations must be safe for concurrent use.
type FS interface {
	// Stat returns a FileInfo describing the named file.
	Stat(name string) (fs.FileInfo, error)

	// MkdirAll creates a directory named path, along with any necessary parents.
	MkdirAll(path string, perm fs.FileMode) error

	// ReadFile reads the named file and returns the contents.
	ReadFile(name string) ([]byte, error)

	// WriteFile writes data to the named file, creating it if necessary.
	WriteFile(name string, data []byte, perm fs.FileMode) error

	// RemoveAll removes path and any children it contains.
	RemoveAll(path string) error

	// Rename renames (moves) oldpath to newpath.
	Rename(oldpath, newpath string) error

	// ReadDir reads the named directory and returns all its directory entries
	// sorted by filename.
	ReadDir(name string) ([]fs.DirEntry, error)
}
