// Package stepcache builds a StepCache from config: off (the default, never hits) or
// dir (one marker file per passed key under a directory), and derives the
// content-addressed key a step is cached under.
package stepcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	// Salt partitions KindDir's keys: a key recorded under one salt never hits under
	// another. A key covers a step's inputs and definition, not the code judging them, so
	// a caller salts with that code's build (BinarySalt) to stop a rebuilt checker
	// trusting its predecessor's passes. Empty is a salt of its own.
	Salt string `yaml:"salt" mapstructure:"salt"`

	// SaltDirs adds the paths and contents of every file under these directories to the
	// salt (a missing one adds nothing): the configuration a checker reads beyond its
	// own row.
	SaltDirs []string `yaml:"salt_dirs" mapstructure:"salt_dirs"`

	// SaltBinary adds the running executable's hash (BinarySalt) to the salt.
	SaltBinary bool `yaml:"salt_binary" mapstructure:"salt_binary"`
}

// NewFromConfig builds the cache cfg.Kind selects. An unknown kind, or KindDir with no
// Dir, is CodeInvalidInput. It does no I/O: a dir cache derives its salt on first use.
// cfg must not be nil.
func NewFromConfig(cfg *Config) (interfaces.StepCache, error) {
	switch cfg.Kind {
	case "", KindOff:
		return off{}, nil
	case KindDir:
		if cfg.Dir == "" {
			return nil, apperr.New(apperr.CodeInvalidInput, "stepcache dir: no directory")
		}

		return newDir(cfg), nil
	default:
		return nil, apperr.New(apperr.CodeInvalidInput,
			"stepcache: unknown kind "+string(cfg.Kind))
	}
}

// Key is the hex SHA-256 of a step's inputs, its tool's version and its arguments. Each
// field is length-prefixed, so no two different field lists share a key.
func Key(inputs []byte, toolVersion string, args []string) string {
	hash := sha256.New()
	field := func(b []byte) {
		var n [8]byte

		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		_, _ = hash.Write(n[:])
		_, _ = hash.Write(b)
	}
	field(inputs)
	field([]byte(toolVersion))

	for _, a := range args {
		field([]byte(a))
	}

	return hex.EncodeToString(hash.Sum(nil))
}

// Digest is the SHA-256 of the paths and contents of the files sel selects in cb, in
// path order: the inputs part of a step's Key.
// sel must not be nil.
func Digest(
	ctx context.Context,
	codebase interfaces.Codebase,
	sel *types.FileSelector,
) ([]byte, error) {
	files, err := codebase.Files(ctx, sel)
	if err != nil {
		return nil, err
	}

	hash := sha256.New()

	for _, file := range files {
		text, err := codebase.Text(ctx, file.Path)
		if err != nil {
			return nil, err
		}

		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(text.Bytes)
		_, _ = hash.Write([]byte{0})
	}

	return hash.Sum(nil), nil
}

// BinarySalt is the hex SHA-256 of the running executable: the Config.Salt that ties
// cached passes to the build that judged them.
func BinarySalt() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "stepcache: locate executable")
	}

	file, err := os.Open(filepath.Clean(exe))
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "stepcache: open "+exe)
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()

	_, err = io.Copy(hash, file)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "stepcache: read "+exe)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
