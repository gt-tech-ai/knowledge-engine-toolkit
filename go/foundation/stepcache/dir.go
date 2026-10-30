package stepcache

import (
	"context"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

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
func newDir(cfg Config) dir {
	return dir{root: cfg.Dir, salt: sync.OnceValues(func() (string, error) {
		return deriveSalt(cfg)
	})}
}

// deriveSalt is cfg's salt: Config.Salt, then the executable's hash under SaltBinary,
// then each salt dir's file paths and contents in walk order, keyed together; just
// Config.Salt when nothing else is configured.
func deriveSalt(cfg Config) (string, error) {
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
	for _, root := range cfg.SaltDirs {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case os.IsNotExist(err) && p == root:
				return filepath.SkipDir
			case err != nil || d.IsDir():
				return err
			}
			b, err := os.ReadFile(p) //nolint:gosec // G304: a configured salt dir's file
			if err != nil {
				return err //nolint:wrapcheck // wrapped once below
			}
			parts = append(parts, filepath.ToSlash(p), string(b))
			return nil
		})
		if err != nil {
			return "", apperr.Wrap(err, apperr.CodeInternal, "stepcache: hash "+root)
		}
	}
	return Key(nil, "", parts), nil
}

// Hit reports whether key's marker exists.
func (d dir) Hit(_ context.Context, key string) (bool, error) {
	p, err := d.path(key)
	if err != nil {
		return false, err
	}
	switch _, err := os.Stat(p); {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, apperr.Wrap(err, apperr.CodeInternal, "stepcache: stat "+p)
	}
}

// Record creates key's marker; an existing marker is kept.
func (d dir) Record(_ context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.root, 0o750); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "stepcache: create "+d.root)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		if err := f.Close(); err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "stepcache: close "+p)
		}
		return nil
	case os.IsExist(err):
		return nil
	default:
		return apperr.Wrap(err, apperr.CodeInternal, "stepcache: record "+p)
	}
}

// path is key's marker path, salted when d has a salt; a key that is not a hex SHA-256
// is CodeInvalidInput, so a key can never name a path outside root.
func (d dir) path(key string) (string, error) {
	if b, err := hex.DecodeString(key); err != nil || len(b) != 32 {
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
