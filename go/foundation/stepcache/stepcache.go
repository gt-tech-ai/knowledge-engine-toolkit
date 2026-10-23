// Package stepcache builds a StepCache from config: off (the default, never hits) or
// dir (one marker file per passed key under a directory), and derives the
// content-addressed key a step is cached under.
package stepcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Kind selects a StepCache implementation.
type Kind string

const (
	// KindOff never hits and records nothing (the default).
	KindOff Kind = "off"

	// KindDir records each passed key as a marker file under Config.Dir.
	KindDir Kind = "dir"
)

// Config selects and locates the cache.
type Config struct {
	// Kind selects the implementation; empty means KindOff.
	Kind Kind `yaml:"kind" mapstructure:"kind"`

	// Dir is KindDir's directory, created on the first Record.
	Dir string `yaml:"dir" mapstructure:"dir"`
}

// NewFromConfig builds the cache cfg.Kind selects. An unknown kind, or KindDir with no
// Dir, is CodeInvalidInput. It does no I/O.
func NewFromConfig(cfg Config) (interfaces.StepCache, error) {
	switch cfg.Kind {
	case "", KindOff:
		return off{}, nil
	case KindDir:
		if cfg.Dir == "" {
			return nil, apperr.New(apperr.CodeInvalidInput, "stepcache dir: no directory")
		}
		return dir{root: cfg.Dir}, nil
	default:
		return nil, apperr.New(apperr.CodeInvalidInput,
			"stepcache: unknown kind "+string(cfg.Kind))
	}
}

// Key is the hex SHA-256 of a step's inputs, its tool's version and its arguments. Each
// field is length-prefixed, so no two different field lists share a key.
func Key(inputs []byte, toolVersion string, args []string) string {
	h := sha256.New()
	field := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	field(inputs)
	field([]byte(toolVersion))
	for _, a := range args {
		field([]byte(a))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Digest is the SHA-256 of the paths and contents of the files sel selects in cb, in
// path order: the inputs part of a step's Key.
func Digest(
	ctx context.Context,
	cb interfaces.Codebase,
	sel types.FileSelector,
) ([]byte, error) {
	files, err := cb.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	for _, f := range files {
		text, err := cb.Text(ctx, f.Path)
		if err != nil {
			return nil, err
		}
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write(text.Bytes)
		h.Write([]byte{0})
	}
	return h.Sum(nil), nil
}
