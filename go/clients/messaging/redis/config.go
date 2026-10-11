// Package redis provides a Redis Pub/Sub messaging integration (publisher +
// subscriber) implementing the core MessagePublisher / MessageConsumer contracts
// over PUBLISH / SUBSCRIBE / PSUBSCRIBE. It shares an injected go-redis client
// (typically the cache tier's Cache.Client()) and never opens its own connection —
// a peer of the sqs and memory messaging backends.
package redis

import (
	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Config holds Redis Pub/Sub messaging configuration. The go-redis Client is
// required and injected at the composition root (typically cache.Cache.Client());
// the messaging client never dials its own connection ("no second connection").
type Config struct {
	// Client is the shared go-redis client used for PUBLISH and SUBSCRIBE. Required.
	Client *goredis.Client

	// Logger reports the subscriber's dispatch-loop and handler errors (the loop
	// runs in a goroutine, so its errors have nowhere else to surface). Nil skips
	// logging.
	Logger interfaces.Logger

	// Metrics, when set, records per-message handle counters + latency on the
	// subscriber via the shared handler-observability decorator. Nil skips metrics.
	Metrics interfaces.Metrics
}

// NewFromConfig builds a Redis Pub/Sub publisher + subscriber over the shared
// injected client (Config.Client). The publisher is returned as the CountingPublisher
// capability (PUBLISH reports a subscriber count) so a fan-out consumer can decide
// cluster-wide delivery; it is RAW (undecorated): cross-cutting resilience/observability
// is applied at the composition root — the parent messaging factory's KindRedis case, or
// a direct consumer's root — never inside the backend (ARCHITECTURE.md#decorators). The
// subscriber is returned as the concrete *Subscriber so callers can reach its dynamic
// Unsubscribe / PSubscribe / PUnsubscribe methods. It fails loudly (via NewPublisher /
// NewSubscriber) if the client is nil.
func NewFromConfig(cfg Config) (interfaces.CountingPublisher, *Subscriber, error) {
	pub, err := NewPublisher(cfg)
	if err != nil {
		return nil, nil, err
	}
	sub, err := NewSubscriber(cfg)
	if err != nil {
		return nil, nil, err
	}
	return pub, sub, nil
}
