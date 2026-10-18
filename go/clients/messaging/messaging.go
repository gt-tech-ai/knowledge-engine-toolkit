// Package messaging builds the message publisher and consumer from config: the
// in-process memory broker (the default), SQS, or Redis Pub/Sub. Every publish runs
// through the client tier (Decorate); the SQS and Redis consumers observe each
// handle. The concrete backends live in the memory/, sqs/ and redis/ subpackages.
package messaging

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindMemory delivers through an in-process broker (the default).
	KindMemory client.Kind = "memory"

	// KindSQS publishes to and consumes from SQS queues (one per topic).
	KindSQS client.Kind = "sqs"

	// KindRedis broadcasts over Redis Pub/Sub (at-most-once).
	KindRedis client.Kind = "redis"

	// family names the kinds tables and is the client name of an unnamed publisher.
	family = "messaging"
)

// Config selects and tunes the messaging backend.
type Config struct {
	// Kind selects the backend; empty means KindMemory.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// SQS is KindSQS's connection: region, endpoint and credentials (env-bound).
	SQS infra.SQSConfig `yaml:"sqs" mapstructure:"sqs"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("messaging").
	Name string `yaml:"name" mapstructure:"name"`

	// Receive tunes the KindSQS consumer's long poll and failed-message backoff.
	Receive Receive `yaml:"receive" mapstructure:"receive"`
}

// Receive tunes how a consumer receives (the poll and backoff fields are KindSQS's).
type Receive struct {
	// NackDelay, when Max is positive, hides a message whose handler failed for a
	// jittered delay before redelivery.
	NackDelay sqs.NackDelay `yaml:"nack_delay" mapstructure:"nack_delay"`

	// BaseBackoff is the initial wait after a failed receive; 0 uses the default.
	BaseBackoff time.Duration `yaml:"base_backoff" mapstructure:"base_backoff"`

	// WaitTimeSeconds is the long-poll wait per receive, 0 to 20; 0 uses the default.
	WaitTimeSeconds int32 `yaml:"wait_time_seconds" mapstructure:"wait_time_seconds"`
}

// Deps carries the messaging runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// Redis is the shared go-redis client KindRedis requires.
	Redis *goredis.Client

	// Broker connects a KindMemory publisher and consumer; nil gives each its own.
	Broker *memory.Broker

	// SQSAPI replaces the SQS SDK client KindSQS builds from Config.SQS.
	SQSAPI sqs.API

	// Ready gates every SQS receive: while it errors the consumer backs off.
	Ready func(ctx context.Context) error
}

// DefaultConfig returns the memory kind in us-east-1.
func DefaultConfig() Config { return Config{SQS: infra.SQSConfig{Region: "us-east-1"}} }

// name is the client name of cfg's publisher ("messaging" when unnamed); its handlers
// are "<name>.handler".
func name(cfg Config) string {
	if cfg.Name == "" {
		return family
	}
	return cfg.Name
}

// sqsConfig is the SQS backend's config: cfg's connection and receive tuning, with
// its runtime collaborators taken from deps and its handler tier handlers.
func sqsConfig(cfg Config, deps Deps, handlers client.Base) sqs.Config {
	return sqs.Config{
		AWSRegion:       cfg.SQS.Region,
		Endpoint:        cfg.SQS.Endpoint,
		AccessKeyID:     cfg.SQS.AccessKeyID,
		SecretAccessKey: cfg.SQS.SecretAccessKey,
		NackDelay:       cfg.Receive.NackDelay,
		BaseBackoff:     cfg.Receive.BaseBackoff,
		WaitTimeSeconds: cfg.Receive.WaitTimeSeconds,
		Logger:          deps.Observability().Logger,
		Handlers:        handlers,
		API:             deps.SQSAPI,
		Ready:           deps.Ready,
	}
}

// redisConfig is the Redis backend's config from deps and its handler tier handlers.
func redisConfig(deps Deps, handlers client.Base) redis.Config {
	return redis.Config{
		Client:   deps.Redis,
		Logger:   deps.Observability().Logger,
		Handlers: handlers,
	}
}

// broker is deps.Broker, or a fresh one.
func broker(deps Deps) *memory.Broker {
	if deps.Broker != nil {
		return deps.Broker
	}
	return memory.NewBroker()
}

// publishers is the publisher backend table.
func publishers() client.Kinds[Config, Deps, interfaces.MessagePublisher] {
	return client.Kinds[Config, Deps, interfaces.MessagePublisher]{
		Family:   family,
		Default:  KindMemory,
		Decorate: Decorate,
		Options:  []client.Option{client.Producer()},
		// PUBLISH is at-most-once, so a retry could double-broadcast.
		AtMostOnce: []client.Kind{KindRedis},
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.MessagePublisher]{
			KindMemory: func(
				_ context.Context, _ Config, deps Deps,
			) (interfaces.MessagePublisher, error) {
				return memory.NewPublisher(broker(deps)), nil
			},
			KindSQS: func(
				ctx context.Context, cfg Config, deps Deps,
			) (interfaces.MessagePublisher, error) {
				return sqs.NewPublisher(ctx, sqsConfig(cfg, deps, client.Base{}))
			},
			KindRedis: func(
				_ context.Context, _ Config, deps Deps,
			) (interfaces.MessagePublisher, error) {
				return redis.NewPublisher(redisConfig(deps, client.Base{}))
			},
		},
	}
}

// consumers is the consumer backend table; handlers runs each handler call.
func consumers(
	handlers client.Base,
) client.Kinds[Config, Deps, interfaces.MessageConsumer] {
	return client.Kinds[Config, Deps, interfaces.MessageConsumer]{
		Family:  family,
		Default: KindMemory,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.MessageConsumer]{
			KindMemory: func(
				_ context.Context, _ Config, deps Deps,
			) (interfaces.MessageConsumer, error) {
				return memory.NewSubscriber(broker(deps), handlers), nil
			},
			KindSQS: func(
				ctx context.Context, cfg Config, deps Deps,
			) (interfaces.MessageConsumer, error) {
				return sqs.NewSubscriber(ctx, sqsConfig(cfg, deps, handlers))
			},
			KindRedis: func(
				_ context.Context, _ Config, deps Deps,
			) (interfaces.MessageConsumer, error) {
				return redis.NewSubscriber(redisConfig(deps, handlers))
			},
		},
	}
}

// NewFromConfig builds the publisher cfg.Kind selects and runs it through the client
// tier. ctx bounds the SQS kind's AWS config load; the other kinds do no I/O. Redis
// publishes are never retried: PUBLISH is at-most-once, so a retry could
// double-broadcast. An unknown kind is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.MessagePublisher, error) {
	return publishers().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, name(cfg))
}

// NewConsumerFromConfig builds the consumer cfg.Kind selects; each backend runs every
// handler call through the handler tier, decorated as the client "<name>.handler"
// (observed and bounded, never retried or broken). An unknown kind is
// CodeInvalidInput.
func NewConsumerFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.MessageConsumer, error) {
	handlers, err := client.NewWireBase(
		deps.Decorations, name(cfg)+".handler", client.Handler(),
	)
	if err != nil {
		return nil, err
	}
	return consumers(handlers).New(ctx, cfg.Kind, cfg, deps)
}
