// Package replaybuffer selects and builds a ReplayBuffer backend from configuration and
// wraps it in the resilience + observability decorator stack, mirroring clients/lock.
// Callers depend on core/interfaces.ReplayBuffer; NewFromConfig chooses the memory
// (single-pod) or redis (cross-pod) backend by Kind and injects it at the composition
// root — swapping one for the other is a config change, never a logic edit.
package replaybuffer

import (
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/memory"
	bufredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Kind selects which ReplayBuffer backend NewFromConfig builds.
type Kind int

const (
	// KindMemory is the in-process backend: correct for a single replica (dev /
	// replica=1), where a reconnect lands on the same pod — no external dependency.
	KindMemory Kind = iota
	// KindRedis is the cross-pod backend over a shared Redis client (staging/prod,
	// multi-replica).
	KindRedis
)

// String returns the config token for the kind.
func (k Kind) String() string {
	switch k {
	case KindMemory:
		return "memory"
	case KindRedis:
		return "redis"
	default:
		return "unknown"
	}
}

// Config selects and tunes the replay buffer. Kind picks the backend; MaxSize bounds the
// retained tail per key; TTL expires an idle key's buffer; OpTimeout bounds each backend
// op (zero skips the timeout decorator). Logger/Metrics/Tracer drive the observability
// decorators (each nil-safe).
type Config struct {
	// Logger drives the logging decorator; nil skips it.
	Logger interfaces.Logger
	// Metrics drives the metrics decorator; nil skips it.
	Metrics interfaces.Metrics
	// Tracer drives the tracing decorator; nil skips it.
	Tracer interfaces.Tracer

	// Name labels the buffer in metrics, spans, and logs (default "replaybuffer").
	Name string
	// KeyPrefix namespaces the redis backend's keys (empty uses redis.DefaultKeyPrefix,
	// "replay:"); ignored for KindMemory.
	KeyPrefix string

	// Kind selects the backend (memory | redis).
	Kind Kind
	// MaxSize bounds the retained messages per key.
	MaxSize int
	// TTL is how long a key's buffer lives after its last Append.
	TTL time.Duration
	// OpTimeout bounds each backend op via the timeout decorator; zero skips it.
	OpTimeout time.Duration
}

// NewFromConfig builds the configured ReplayBuffer: it selects a backend by Kind, wraps
// it in the timeout + observability decorator stack (each collaborator
// optional/nil-safe), and returns the decorated buffer. The client is required only for
// KindRedis (the shared go-redis client reused from the lock/backplane); it is ignored
// for KindMemory. It fails loudly on a non-positive MaxSize/TTL, an unknown kind, or a
// redis kind with no client.
func NewFromConfig(cfg *Config, client *goredis.Client) (interfaces.ReplayBuffer, error) {
	if cfg.MaxSize <= 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"replay max_size must be positive",
		)
	}
	if cfg.TTL <= 0 {
		return nil, apperr.New(apperr.CodeInvalidInput, "replay ttl must be positive")
	}

	var base interfaces.ReplayBuffer
	switch cfg.Kind {
	case KindMemory:
		base = memory.New(cfg.MaxSize, cfg.TTL)
	case KindRedis:
		if client == nil {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"replay redis kind requires a redis client",
			)
		}
		base = bufredis.New(client, bufredis.Config{
			MaxSize:   cfg.MaxSize,
			TTL:       cfg.TTL,
			KeyPrefix: cfg.KeyPrefix,
		})
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"unknown replay buffer kind: "+cfg.Kind.String(),
		)
	}

	name := cfg.Name
	if name == "" {
		name = "replaybuffer"
	}
	return decorators.NewBuilder(base, name).
		WithTimeout(cfg.OpTimeout).
		WithLogging(cfg.Logger).
		WithMetrics(cfg.Metrics).
		WithTracing(cfg.Tracer).
		Build(), nil
}
