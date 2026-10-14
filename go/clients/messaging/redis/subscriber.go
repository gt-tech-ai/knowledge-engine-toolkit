package redis

import (
	"context"
	"sync"

	goredis "github.com/redis/go-redis/v9"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
)

// Compile-time interface assertions.
var (
	// Subscriber satisfies the base MessageConsumer contract.
	_ interfaces.MessageConsumer = (*Subscriber)(nil)
	// Subscriber also satisfies DynamicConsumer (channels added/removed at runtime).
	_ interfaces.DynamicConsumer = (*Subscriber)(nil)
	// Subscriber also satisfies PatternConsumer (PSUBSCRIBE pattern subscriptions).
	_ interfaces.PatternConsumer = (*Subscriber)(nil)
)

// Subscriber consumes Redis Pub/Sub messages over ONE multiplexed subscription.
// Channels (SUBSCRIBE) and patterns (PSUBSCRIBE) are added and removed at runtime;
// a single dispatch goroutine reads the shared message stream and routes each
// message to the registered handler by exact channel, then by matched pattern.
// Pub/Sub has no ack/nack, so a handler error is logged and the message is
// dropped — never requeued. It holds the shared go-redis client
// and opens no connection of its own.
type Subscriber struct {
	// NoOp supplies the no-op Start/Stop: the subscription lifecycle is owned by
	// Subscribe (lazy start) and Close, so the Subscriber opens no shared resource.
	lifecycle.NoOp

	// logger reports handler failures; nil skips logging.
	logger interfaces.Logger

	// client is the shared go-redis client (injected via Config.Client).
	client *goredis.Client

	// pubsub is the single multiplexed subscription, created lazily on the first
	// Subscribe/PSubscribe and torn down by Close.
	pubsub *goredis.PubSub

	// cancel cancels the subscriber-lifetime context passed to handlers, so
	// in-flight handlers observe shutdown when Close is called; nil until the first
	// subscribe. (Only the CancelFunc is stored, not the context — containedctx.)
	cancel context.CancelFunc

	// channels maps an exact channel name to its wrapped handler.
	channels map[string]interfaces.MessageHandler

	// patterns maps a glob pattern to its wrapped handler.
	patterns map[string]interfaces.MessageHandler

	// handlers runs each handle through the client tier (op = the channel).
	handlers client.Base

	// wg tracks the dispatch goroutine so Close drains it.
	wg sync.WaitGroup

	// mu guards pubsub, cancel, the handler registries, and closed.
	mu sync.RWMutex

	// closed is set by Close; a Subscribe after Close is a no-op error, not a panic.
	closed bool
}

// NewSubscriber creates a Redis Pub/Sub subscriber over the injected client. It
// returns a coded InvalidInput error if the client is nil.
func NewSubscriber(cfg Config) (*Subscriber, error) {
	if cfg.Client == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, "redis messaging: nil client")
	}
	return &Subscriber{
		client:   cfg.Client,
		logger:   cfg.Logger,
		handlers: cfg.Handlers,
		channels: make(map[string]interfaces.MessageHandler),
		patterns: make(map[string]interfaces.MessageHandler),
	}, nil
}

// Subscribe starts delivering messages from the exact channel topic to handler,
// adding it to the multiplexed subscription at runtime.
func (s *Subscriber) Subscribe(
	ctx context.Context, topic string, handler interfaces.MessageHandler,
) error {
	ps, err := s.register(ctx, s.channels, topic, handler)
	if err != nil {
		return err
	}
	if err := ps.Subscribe(ctx, topic); err != nil {
		s.rollback(s.channels, topic)
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis subscribe")
	}
	return nil
}

// Unsubscribe stops delivering messages from channel topic. It is a no-op if topic
// was never subscribed (or the subscriber never started).
func (s *Subscriber) Unsubscribe(ctx context.Context, topic string) error {
	ps, ok := s.unregister(s.channels, topic)
	if ps == nil || !ok {
		return nil
	}
	if err := ps.Unsubscribe(ctx, topic); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis unsubscribe")
	}
	return nil
}

// PSubscribe starts delivering messages from every channel matching pattern (glob
// syntax) to handler, adding the pattern to the multiplexed subscription at runtime.
func (s *Subscriber) PSubscribe(
	ctx context.Context, pattern string, handler interfaces.MessageHandler,
) error {
	ps, err := s.register(ctx, s.patterns, pattern, handler)
	if err != nil {
		return err
	}
	if err := ps.PSubscribe(ctx, pattern); err != nil {
		s.rollback(s.patterns, pattern)
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis psubscribe")
	}
	return nil
}

// PUnsubscribe stops delivering messages for pattern. It is a no-op if pattern was
// never subscribed (or the subscriber never started).
func (s *Subscriber) PUnsubscribe(ctx context.Context, pattern string) error {
	ps, ok := s.unregister(s.patterns, pattern)
	if ps == nil || !ok {
		return nil
	}
	if err := ps.PUnsubscribe(ctx, pattern); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis punsubscribe")
	}
	return nil
}

// register lazily starts the subscription (deriving its lifetime context from ctx),
// records handler under reg[key], and returns the pubsub
// to issue the broker (P)SUBSCRIBE on. It returns a coded error if the subscriber is
// already closed.
func (s *Subscriber) register(
	ctx context.Context,
	reg map[string]interfaces.MessageHandler,
	key string,
	handler interfaces.MessageHandler,
) (*goredis.PubSub, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, apperr.New(apperr.CodeUnavailable, "redis subscriber closed")
	}
	s.ensureStartedLocked(ctx)
	reg[key] = handler
	return s.pubsub, nil
}

// rollback removes reg[key] after a failed broker (P)SUBSCRIBE so a dropped
// subscription leaves no orphaned handler.
func (s *Subscriber) rollback(reg map[string]interfaces.MessageHandler, key string) {
	s.mu.Lock()
	delete(reg, key)
	s.mu.Unlock()
}

// unregister removes reg[key] and returns the pubsub plus whether key was present,
// so the caller can issue the broker (P)UNSUBSCRIBE only for a live subscription.
func (s *Subscriber) unregister(
	reg map[string]interfaces.MessageHandler, key string,
) (*goredis.PubSub, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := reg[key]
	delete(reg, key)
	return s.pubsub, ok
}

// Close stops the dispatch loop and tears down the subscription. It is idempotent
// and nil-safe (Close before any Subscribe is a no-op).
func (s *Subscriber) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ps := s.pubsub
	s.pubsub = nil
	cancel := s.cancel
	s.mu.Unlock()

	// Signal in-flight handlers that the subscriber is shutting down.
	if cancel != nil {
		cancel()
	}
	if ps == nil {
		return nil
	}
	// Closing the pubsub closes the channel returned by Channel(), so the dispatch
	// goroutine's range loop exits; wait for it to drain.
	err := ps.Close()
	s.wg.Wait()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis subscriber close")
	}
	return nil
}

// ensureStartedLocked lazily creates the one multiplexed subscription and launches
// the dispatch goroutine on first use. The caller must hold s.mu (write).
func (s *Subscriber) ensureStartedLocked(ctx context.Context) {
	if s.pubsub != nil {
		return
	}
	// A subscriber-lifetime context, cancelled by Close, is passed to every handler
	// so an in-flight handler can observe shutdown. It is derived from the first
	// subscribing call's ctx without its cancellation: the dispatch loop outlives that
	// call, so the call ending must not stop delivery to every other subscription.
	// WithoutCancel keeps that call's context values, so its trace span, tenant and
	// any other request-scoped value reach every handler's ctx for the subscriber's
	// whole life; a handler must read per-message identity from the message itself.
	dispatchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	// Subscribe with no channels: the subscription is opened, and channels/patterns
	// are added dynamically by Subscribe/PSubscribe.
	s.pubsub = s.client.Subscribe(dispatchCtx)
	ch := s.pubsub.Channel()
	s.wg.Add(1)
	go s.dispatch(dispatchCtx, ch)
}

// dispatch routes each message to its handler — exact channel first, then matched
// pattern — and returns when the pubsub channel closes (on Close). Handlers receive
// the subscriber-lifetime ctx (cancelled by Close). Each handle runs through the client
// tier as the operation named by its channel; a handler error is logged and dropped
// (Pub/Sub has no requeue).
func (s *Subscriber) dispatch(ctx context.Context, ch <-chan *goredis.Message) {
	defer s.wg.Done()
	for msg := range ch {
		s.mu.RLock()
		handler, ok := s.channels[msg.Channel]
		if !ok && msg.Pattern != "" {
			handler, ok = s.patterns[msg.Pattern]
		}
		s.mu.RUnlock()
		if !ok {
			continue
		}
		m := &interfaces.Message{Topic: msg.Channel, Payload: []byte(msg.Payload)}
		err := s.handlers.DoOnce(ctx, m.Topic, func(ctx context.Context) error {
			return handler(ctx, m)
		})
		if err != nil && s.logger != nil {
			s.logger.WithContext(ctx).Error(
				"message handle failed", "topic", m.Topic, "error", err,
			)
		}
	}
}
