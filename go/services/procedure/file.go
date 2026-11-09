package procedure

import (
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
)

// The default permissions of what a file step creates.
const (
	// defaultFileMode is a created file's permission when the step sets no Mode.
	defaultFileMode fs.FileMode = 0o644

	// defaultDirMode is a created directory's permission when the step sets no Mode,
	// and the permission of the parents a file op creates.
	defaultDirMode fs.FileMode = 0o755
)

// validFile checks f's op, that it names a path, that only a copy has (and needs) a
// From, and that its Mode is octal.
func validFile(f *types.ProcFile) error {
	switch {
	case f.Op != types.FileEnsure && f.Op != types.FileCopy &&
		f.Op != types.FileMkdir && f.Op != types.FileRemove:
		return apperr.New(apperr.CodeInvalidInput, "unknown file op "+strconv.Quote(f.Op))
	case f.Path == "":
		return apperr.New(apperr.CodeInvalidInput, "a file step names a path")
	case (f.Op == types.FileCopy) != (f.From != ""):
		return apperr.New(apperr.CodeInvalidInput, "from is set exactly on a copy")
	}

	_, err := mode(f.Mode, 0)

	return err
}

// mode parses an octal permission; empty is def.
func mode(s string, def fs.FileMode) (fs.FileMode, error) {
	if s == "" {
		return def, nil
	}

	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > uint64(fs.ModePerm) {
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			"file mode "+s+" is not octal 0-0777",
		)
	}

	return fs.FileMode(n), nil
}

// file applies f under the root and returns its row: a pass naming what it did, or a
// failure naming the path.
func (e *engine) file(
	c *compiled,
	f *types.ProcFile,
	name string,
	vars Vars,
) types.StepResult {
	msg, err := e.applyFile(f, vars)
	if err != nil {
		return c.result(name, types.StatusFail, err.Error())
	}

	res := c.result(name, types.StatusPass, "")

	res.Detail = []byte(msg)

	return res
}

// applyFile expands f's paths, confines them to the root and runs its op.
func (e *engine) applyFile(f *types.ProcFile, vars Vars) (string, error) {
	path, err := e.rooted(f.Path, vars)
	if err != nil {
		return "", err
	}

	files := e.fs()

	switch f.Op {
	case types.FileEnsure:
		return ensureFile(files, path, f.Mode)
	case types.FileCopy:
		from, fromErr := e.rooted(f.From, vars)
		if fromErr != nil {
			return "", fromErr
		}

		return copyFile(files, from, path, f.Mode)
	case types.FileMkdir:
		return makeDir(files, path, f.Mode)
	default:
		return removePath(files, path)
	}
}

// makeDir creates path and its parents.
func makeDir(files interfaces.FS, path, perm string) (string, error) {
	dirMode, _ := mode(perm, defaultDirMode)

	err := files.MkdirAll(path, dirMode)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "create "+path)
	}

	return "created " + path, nil
}

// removePath removes path and anything under it.
func removePath(files interfaces.FS, path string) (string, error) {
	err := files.RemoveAll(path)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "remove "+path)
	}

	return "removed " + path, nil
}

// fs is the filesystem file steps use.
func (e *engine) fs() interfaces.FS {
	if e.deps.FS != nil {
		return e.deps.FS
	}

	return fsys.DefaultFS()
}

// rooted expands rel and joins it to the root; a path that leaves the root is
// CodeInvalidInput.
func (e *engine) rooted(rel string, vars Vars) (string, error) {
	p, err := text(rel, vars)
	if err != nil {
		return "", err
	}

	if !filepath.IsLocal(p) {
		return "", apperr.New(apperr.CodeInvalidInput, p+" is outside the root")
	}

	return filepath.Join(e.deps.Root, p), nil
}

// ensureFile creates path as an empty file when missing, replacing an empty directory
// in its place; a non-empty directory is CodeConflict and is left alone.
func ensureFile(files interfaces.FS, path, perm string) (string, error) {
	info, err := files.Stat(path)

	switch {
	case err == nil && !info.IsDir():
		return path + " exists", nil
	case err == nil:
		clearErr := clearEmptyDir(files, path)
		if clearErr != nil {
			return "", clearErr
		}
	case !apperr.StdIs(err, fs.ErrNotExist):
		return "", apperr.Wrap(err, apperr.CodeInternal, "stat "+path)
	default:
		// No other value changes the outcome.
	}

	return "created " + path, write(files, path, nil, perm)
}

// clearEmptyDir removes an empty directory squatting on a file's path; a non-empty
// one is a conflict the caller must resolve.
func clearEmptyDir(files interfaces.FS, path string) error {
	entries, err := files.ReadDir(path)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "read "+path)
	}

	if len(entries) > 0 {
		return apperr.New(apperr.CodeConflict,
			path+" is a non-empty directory, not a file; remove it")
	}

	err = files.RemoveAll(path)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "remove "+path)
	}

	return nil
}

// copyFile copies from to path unless path exists; a missing from is CodeNotFound.
func copyFile(files interfaces.FS, from, path, perm string) (string, error) {
	_, err := files.Stat(path)
	if err == nil {
		return path + " exists", nil
	}

	data, err := files.ReadFile(from)
	if err != nil {
		code := apperr.CodeInternal
		if apperr.StdIs(err, fs.ErrNotExist) {
			code = apperr.CodeNotFound
		}

		return "", apperr.Wrap(err, code, "read "+from)
	}

	return "copied " + from + " to " + path, write(files, path, data, perm)
}

// write creates path's directory and writes data with perm (default 0644).
func write(files interfaces.FS, path string, data []byte, perm string) error {
	m, _ := mode(perm, defaultFileMode)

	err := files.MkdirAll(filepath.Dir(path), defaultDirMode)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create the directory of "+path)
	}

	err = files.WriteFile(path, data, m)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "write "+path)
	}

	return nil
}
