// Package storage builds the StorageClient from config: the in-memory backend (the
// default) or an S3-compatible one (AWS S3 or MinIO), run through the client tier
// (Decorate). The concrete backends live in the memory/ and s3/ subpackages.
package storage

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindMemory keeps objects in process (the default; no presigning).
	KindMemory client.Kind = "memory"

	// KindS3 uses an S3-compatible service; S3.Kind picks AWS S3 or MinIO.
	KindS3 client.Kind = "s3"
)

// Config selects and tunes the storage client.
type Config struct {
	// Kind selects the backend; empty means KindMemory.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("storage").
	Name string `yaml:"name" mapstructure:"name"`

	// S3 configures KindS3: the flavor (AWS S3 or MinIO), endpoint and credentials.
	S3 infra.S3Config `yaml:"s3" mapstructure:"s3"`

	// MultipartThreshold is the object size (bytes) above which Upload switches to
	// multipart; 0 uses the default (5 MiB).
	MultipartThreshold int64 `yaml:"multipart_threshold" mapstructure:"multipart_threshold"`
}

// Deps carries the storage runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// API replaces the S3 SDK client KindS3 builds from Config.S3 (presigning is
	// unavailable through it).
	API s3.S3API
}

// DefaultConfig returns the memory kind.
func DefaultConfig() Config { return Config{} }

// kinds is the storage backend table.
func kinds() client.Kinds[Config, Deps, interfaces.StorageClient] {
	return client.Kinds[Config, Deps, interfaces.StorageClient]{
		Family:   "storage",
		Default:  KindMemory,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.StorageClient]{
			KindMemory: func(context.Context, Config, Deps) (interfaces.StorageClient, error) {
				return memory.New(), nil
			},
			KindS3: newS3,
		},
	}
}

// newS3 builds the S3 backend over deps.API, or over the real SDK client cfg.S3
// configures (validated first).
func newS3(ctx context.Context, cfg Config, deps Deps) (interfaces.StorageClient, error) {
	if deps.API != nil {
		return s3.NewClientFromAPI(deps.API, cfg.MultipartThreshold), nil
	}
	if err := cfg.S3.Validate(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "invalid s3 config")
	}
	awsClient, err := s3.NewAWSClient(ctx, cfg.S3)
	if err != nil {
		return nil, err
	}
	return s3.NewClient(awsClient, cfg.S3.PublicEndpoint), nil
}

// NewFromConfig builds the storage client cfg.Kind selects and runs it through the
// client tier. ctx bounds the S3 kind's AWS config load. An unknown kind or an invalid
// S3 config is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.StorageClient, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
