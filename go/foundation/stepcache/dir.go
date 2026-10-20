package stepcache

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// dir records each passed key as an empty marker file named by the key.
type dir struct {
	// root holds the marker files.
	root string
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

// path is key's marker path; a key that is not a hex SHA-256 is CodeInvalidInput, so a
// key can never name a path outside root.
func (d dir) path(key string) (string, error) {
	if b, err := hex.DecodeString(key); err != nil || len(b) != 32 {
		return "", apperr.New(apperr.CodeInvalidInput, "stepcache: malformed key "+key)
	}
	return filepath.Join(d.root, key), nil
}
