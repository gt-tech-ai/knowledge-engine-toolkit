// Package storage provides the StorageClient factory. It selects an
// S3-compatible backend (AWS S3 or MinIO) by Kind and wraps it with the storage
// decorators; the concrete backend lives in the s3/ subpackage.
package storage

import (
	"context"
	"fmt"

	clientstack "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// Kind selects the storage-tier backend: the real S3-compatible client or an in-memory
// stub.
//
// This is distinct from infra.S3Config.Kind, which selects the S3 *flavor* (AWS "s3" vs
// local "minio") WITHIN the S3 backend. KindS3 uses the S3 backend (flavor picked by
// S3Config.Kind); KindMemory uses the in-memory backend and ignores the S3 settings
// entirely.
type Kind int

const (
	// KindS3 uses the real S3-compatible backend (AWS S3 or MinIO). The zero value.
	KindS3 Kind = iota
	// KindMemory uses the in-memory stub backend (dev/test/all-stubs; no external storage).
	KindMemory
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindS3:
		return "s3"
	case KindMemory:
		return "memory"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config configures a StorageClient. It mirrors the SQS client's Config.API seam:
// when API is non-nil it is used directly (a generated mock in unit tests, so the
// client logic runs without a network — presigning is unavailable on the mock
// path); when API is nil, New builds the real aws-sdk-go-v2 client from S3.
type Config struct {
	// API, when set, is the injected S3 API seam (s3.S3API) used instead of a real
	// client.
	API s3.S3API

	// S3 holds the backend selection + credentials used to build the real client
	// when API is nil.
	S3 infra.S3Config

	// MultipartThreshold overrides the object size (bytes) above which Upload
	// switches to multipart; 0 uses the default (5 MiB). Tests set a small value to
	// exercise the multipart path without a multi-megabyte body.
	MultipartThreshold int64

	// Kind selects the storage-tier backend (KindS3 real vs KindMemory stub). The zero
	// value is KindS3, so existing callers keep the S3 backend unchanged.
	Kind Kind
}

// New constructs a StorageClient from cfg: it uses the injected cfg.API when present,
// and otherwise builds the real S3 client from cfg.S3 (validated first). The backend
// (AWS S3 or MinIO) is selected by cfg.S3.Kind, which S3Config.Validate constrains to
// "s3" or "minio".
func New(ctx context.Context, cfg Config) (interfaces.StorageClient, error) {
	if cfg.Kind == KindMemory {
		// The in-memory backend ignores the S3 settings entirely (no validation, no network).
		return memory.New(), nil
	}
	if cfg.API != nil {
		return s3.NewClientFromAPI(cfg.API, cfg.MultipartThreshold), nil
	}
	if err := cfg.S3.Validate(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "invalid s3 config")
	}
	client, err := s3.NewAWSClient(ctx, cfg.S3)
	if err != nil {
		return nil, err
	}
	return s3.NewClient(client, cfg.S3.PublicEndpoint), nil
}

// NewFromConfig constructs the storage-tier StorageClient selected by kind — the real S3
// backend (KindS3, built from cfg) or the in-memory stub (KindMemory, ignoring cfg) — and
// wraps it with the shared client resilience stack (production defaults). It is the
// app-wiring entrypoint; New is the seam-injectable form used by unit tests (which
// returns the bare client so a mock API can be driven without resilience layers).
func NewFromConfig(
	ctx context.Context,
	kind Kind,
	cfg infra.S3Config,
) (interfaces.StorageClient, error) {
	base, err := New(ctx, Config{S3: cfg, Kind: kind})
	if err != nil {
		return nil, err
	}
	return decorators.DecorateFromConfig(
		base,
		kind.String(),
		clientstack.DefaultConfig(),
		clientstack.Deps{},
	)
}
