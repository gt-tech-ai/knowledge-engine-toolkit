package stepcache

import (
	"context"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// groupDirMode is the mode of a directory its group may read.
const groupDirMode = 0o750

// privateFileMode is the mode of a file only its owner reads.
const privateFileMode = 0o600

// dir records each passed key as an empty marker file named by the key, or by the
// key's hash under the salt when there is one.
type dir struct {
	// salt derives the salt partitioning the keys once, on first use (Config.Salt,
	// SaltDirs, SaltBinary); empty when none is configured.
	salt func() (string, error)

	// root holds the marker files.
	root string
}

// newDir returns the dir cache cfg configures, its salt derived lazily.
func newDir(cfgRef *Config) dir {
	cfg := *cfgRef

	return dir{root: cfg.Dir, salt: sync.OnceValues(func() (string, error) {
		return deriveSalt(&cfg)
	})}
}

// deriveSalt is cfg's salt: Config.Salt, then the executable's hash under SaltBinary,
// then each salt dir's file paths and contents in walk order, keyed together; just
// Config.Salt when nothing else is configured.
func deriveSalt(cfg *Config) (string, error) {
	if !cfg.SaltBinary && len(cfg.SaltDirs) == 0 {
		return cfg.Salt, nil
	}

	parts := []string{cfg.Salt}
	if cfg.SaltBinary {
		bin, err := BinarySalt()
		if err != nil {
			return "", err
		}

		parts = append(parts, bin)
	}

	parts, err := appendSaltDirs(parts, cfg.SaltDirs)
	if err != nil {
		return "", err
	}

	return Key(nil, "", parts), nil
}

// appendSaltDirs adds every root's files to the salt parts.
func appendSaltDirs(parts, roots []string) ([]string, error) {
	var err error

	for _, root := range roots {
		parts, err = appendSaltDir(parts, root)
		if err != nil {
			return nil, err
		}
	}

	return parts, nil
}

// appendSaltDir appends each file's path and contents under root, in walk order, to
// parts. The walk reads through an os.Root, so a symlink inside root cannot lead a read
// outside it. A root that does not exist contributes nothing.
func appendSaltDir(parts []string, root string) ([]string, error) {
	rootValue, err := os.OpenRoot(root)
	if os.IsNotExist(err) {
		return parts, nil
	}

	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "stepcache: open "+root)
	}

	defer func() { _ = rootValue.Close() }()

	fsys := rootValue.FS()

	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		return appendSaltFile(&parts, fsys, root, p)
	})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "stepcache: hash "+root)
	}

	return parts, nil
}

// appendSaltFile adds one file's root-qualified path and content to the salt parts.
func appendSaltFile(parts *[]string, fsys fs.FS, root, text string) error {
	b, err := fs.ReadFile(fsys, text)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "stepcache: read "+text)
	}

	*parts = append(*parts, filepath.ToSlash(filepath.Join(root, text)), string(b))

	return nil
}

// Hit reports whether key's marker exists.
func (d dir) Hit(_ context.Context, key string) (bool, error) {
	text, err := d.path(key)
	if err != nil {
		return false, err
	}

	switch _, err := os.Stat(text); {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, apperr.Wrap(err, apperr.CodeInternal, "stepcache: stat "+text)
	}
}

// Record creates key's marker; an existing marker is kept.
func (d dir) Record(_ context.Context, key string) error {
	text, err := d.path(key)
	if err != nil {
		return err
	}

	err = os.MkdirAll(d.root, groupDirMode)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "stepcache: create "+d.root)
	}

	file, err := os.OpenFile(
		filepath.Clean(text),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		privateFileMode,
	)
	switch {
	case err == nil:
		opErr := file.Close()
		if opErr != nil {
			return apperr.Wrap(opErr, apperr.CodeInternal, "stepcache: close "+text)
		}

		return nil
	case os.IsExist(err):
		return nil
	default:
		return apperr.Wrap(err, apperr.CodeInternal, "stepcache: record "+text)
	}
}

// path is key's marker path, salted when d has a salt; a key that is not a hex SHA-256
// is CodeInvalidInput, so a key can never name a path outside root.
func (d dir) path(key string) (string, error) {
	b, err := hex.DecodeString(key)
	if err != nil || len(b) != bLen32 {
		return "", apperr.New(apperr.CodeInvalidInput, "stepcache: malformed key "+key)
	}

	salt, err := d.salt()
	if err != nil {
		return "", err
	}

	if salt != "" {
		key = Key([]byte(salt), "", []string{key})
	}

	return filepath.Join(d.root, key), nil
}
