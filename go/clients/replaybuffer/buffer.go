// Package replaybuffer builds a ReplayBuffer from config — the in-process memory buffer
// (single pod; the default) or Redis (cross-pod) — and runs it through the client tier
// (Decorate). Callers depend on core/interfaces.ReplayBuffer, so swapping one backend
// for the other is a config change, never a logic edit. The concrete backends live in
// the memory/ and redis/ subpackages.
package replaybuffer

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/memory"
	bufredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindMemory keeps each key's tail in process: correct for a single pod (the
	// default).
	KindMemory client.Kind = "memory"

	// KindRedis keeps each key's tail in Redis, shared across pods.
	KindRedis client.Kind = "redis"
)

// Config selects and tunes the replay buffer.
type Config struct {
	// Kind selects the backend; empty means KindMemory.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// KeyPrefix namespaces the redis backend's keys (empty uses
	// redis.DefaultKeyPrefix, "replay:"); ignored for KindMemory.
	KeyPrefix string `yaml:"key_prefix" mapstructure:"key_prefix"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("replaybuffer").
	Name string `yaml:"name" mapstructure:"name"`

	// MaxSize bounds the retained messages per key.
	MaxSize int `yaml:"max_size" mapstructure:"max_size"`

	// TTL is how long a key's buffer lives after its last Append.
	TTL time.Duration `yaml:"ttl" mapstructure:"ttl"`
}

// Deps carries the replay buffer's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// Redis is the shared go-redis client KindRedis requires.
	Redis *goredis.Client
}

// kinds is the replay-buffer backend table.
func kinds() client.Kinds[Config, Deps, interfaces.ReplayBuffer] {
	return client.Kinds[Config, Deps, interfaces.ReplayBuffer]{
		Family:   "replaybuffer",
		Default:  KindMemory,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.ReplayBuffer]{
			KindMemory: func(
				_ context.Context, cfg Config, _ Deps,
			) (interfaces.ReplayBuffer, error) {
				return memory.New(cfg.MaxSize, cfg.TTL), nil
			},
			KindRedis: func(
				_ context.Context, cfg Config, deps Deps,
			) (interfaces.ReplayBuffer, error) {
				if deps.Redis == nil {
					return nil, apperr.New(
						apperr.CodeInvalidInput,
						"replay redis kind requires a redis client",
					)
				}
				return bufredis.New(deps.Redis, bufredis.Config{
					MaxSize:   cfg.MaxSize,
					TTL:       cfg.TTL,
					KeyPrefix: cfg.KeyPrefix,
				}), nil
			},
		},
	}
}

// NewFromConfig builds the replay buffer cfg.Kind selects and runs it through the
// client tier. A non-positive MaxSize or TTL, an unknown kind, or KindRedis without
// Deps.Redis is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.ReplayBuffer, error) {
	if cfg.MaxSize <= 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"replay max_size must be positive",
		)
	}
	if cfg.TTL <= 0 {
		return nil, apperr.New(apperr.CodeInvalidInput, "replay ttl must be positive")
	}
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
