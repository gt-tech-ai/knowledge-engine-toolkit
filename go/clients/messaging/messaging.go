// Package messaging provides a factory for message publisher and subscriber
// implementations with multiple backends.
//
// Use NewFromConfig() for app wiring (the tier-root factory that builds a
// publisher straight from the loaded config), or the lower-level NewPublisher(),
// NewSubscriber(), and NewAdapter() constructors. The factory selects the
// implementation at runtime based on Kind; backend impls live in the sqs/
// subpackage.
//
// Example:
//
//	pub, err := messaging.NewPublisher(messaging.KindSQS, messaging.WithQueueURL(url))
//	if err != nil {
//	    log.Fatal(err)
//	}
package messaging

import (
	"context"
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which messaging implementation to use.
type Kind int

const (
	// KindSQS uses AWS SQS for message publishing and subscription. Suitable for
	// distributed deployments with AWS infrastructure.
	KindSQS Kind = iota

	// KindMemory uses an in-process broker (dev/test/all-stubs; no external broker).
	KindMemory

	// KindRedis uses Redis Pub/Sub (fire-and-forget broadcast + dynamic SUBSCRIBE/
	// PSUBSCRIBE). Its go-redis client is injected via WithRedisClient (shared with
	// the cache tier), not built from config — the backend for cross-replica fan-out
	// (e.g. pushing a message to whichever replica holds a client connection).
	KindRedis
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindSQS:
		return "sqs"
	case KindMemory:
		return "memory"
	case KindRedis:
		return "redis"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// ParseKind maps a config kind string ("sqs" | "memory" | "redis") to its Kind,
// failing loudly on an unknown value. It is the messaging-tier bridge a composition
// root uses to select a backend from configuration
// (ARCHITECTURE.md#swappable-components); the backend config (e.g. the injected Redis
// client) is still supplied separately via options.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "sqs":
		return KindSQS, nil
	case "memory":
		return KindMemory, nil
	case "redis":
		return KindRedis, nil
	default:
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown messaging kind: %q", s),
		)
	}
}

// NewFromConfig builds a MessagePublisher selected by the tier kind — the SQS backend
// (KindSQS, configured from cfg), the in-process stub (KindMemory, ignoring cfg), or
// Redis Pub/Sub (KindRedis, whose shared go-redis client is injected via the
// WithRedisClient option — NOT from cfg; the SQSConfig arg is inert for Redis) — wrapping
// the thin core in the messaging decorators. It is the app-wiring entrypoint, sitting at
// the tier root above the backends exactly like cache/storage NewFromConfig; additional
// options (logger, metrics, retrier, redis client) may be layered on. The context
// bounds the SQS backend's AWS config load; the other backends do no I/O at
// construction.
func NewFromConfig(
	ctx context.Context,
	kind Kind,
	cfg infra.SQSConfig,
	opts ...options.Option[Config],
) (interfaces.MessagePublisher, error) {
	base := make([]options.Option[Config], 0, 3+len(opts))
	base = append(base,
		WithAWSRegion(cfg.Region),
		WithEndpoint(cfg.Endpoint),
		WithStaticCredentials(cfg.AccessKeyID, cfg.SecretAccessKey),
	)
	return newPublisher(ctx, kind, append(base, opts...)...)
}

// NewPublisher creates a message publisher of the specified kind with optional
// functional options. Returns an error if the kind is unknown.
func NewPublisher(
	kind Kind,
	opts ...options.Option[Config],
) (interfaces.MessagePublisher, error) {
	return newPublisher(context.Background(), kind, opts...)
}

// newPublisher builds the publisher for kind, constructing the SQS backend's AWS
// client under ctx.
func newPublisher(
	ctx context.Context,
	kind Kind,
	opts ...options.Option[Config],
) (interfaces.MessagePublisher, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)

	switch cfg.Kind {
	case KindSQS:
		core, err := sqs.NewPublisherContext(ctx, cfg.SQS)
		if err != nil {
			return nil, err
		}
		// Cross-cutting concerns (logging, metrics, resilience) compose around the
		// thin SQS core as decorators, opted in by whichever deps were wired.
		return decorators.WrapPublisher(core, decorators.PublisherDeps{
			Logger:  cfg.Logger,
			Metrics: cfg.Metrics,
			Retrier: cfg.Retrier,
		}), nil

	case KindMemory:
		// The in-process publisher ignores the SQS settings; a subscriber built from the
		// same broker receives its messages (the roundtrip path is exercised via direct
		// construction).
		return memory.NewPublisher(memory.NewBroker()), nil

	case KindRedis:
		core, err := redis.NewPublisher(cfg.Redis)
		if err != nil {
			return nil, err
		}
		// Same shared resilience/observability stack as the other backends, with the
		// Retrier intentionally omitted: Redis PUBLISH is fire-and-forget / at-most-once,
		// so a retry could double-broadcast to already-delivered subscribers.
		return decorators.WrapPublisher(core, decorators.PublisherDeps{
			Logger:  cfg.Logger,
			Metrics: cfg.Metrics,
		}), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown messaging kind: %v", cfg.Kind),
		)
	}
}

// NewSubscriber creates a message subscriber of the specified kind with
// optional functional options. Returns an error if the kind is unknown.
func NewSubscriber(
	kind Kind,
	opts ...options.Option[Config],
) (interfaces.MessageConsumer, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)

	switch cfg.Kind {
	case KindSQS:
		return sqs.NewSubscriber(cfg.SQS)

	case KindMemory:
		return memory.NewSubscriber(memory.NewBroker()), nil

	case KindRedis:
		return redis.NewSubscriber(cfg.Redis)

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown messaging kind: %v", cfg.Kind),
		)
	}
}

// NewAdapter creates a messaging adapter of the specified kind with optional
// functional options. Returns an error if the kind is unknown.
func NewAdapter(kind Kind, opts ...options.Option[Config]) (*sqs.Adapter, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)

	switch cfg.Kind {
	case KindSQS:
		return sqs.NewAdapter(cfg.SQS), nil

	case KindMemory, KindRedis:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("messaging kind %v has no adapter (only sqs does)", cfg.Kind),
		)

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown messaging kind: %v", cfg.Kind),
		)
	}
}
