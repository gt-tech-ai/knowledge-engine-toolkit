//go:build integration

// Package integration verifies the Redis Pub/Sub messaging backend
// (go/clients/messaging/redis) against a real Redis instance (redis:7-alpine
// via testcontainers). The backend wraps a concrete go-redis client with no
// injectable seam, so its PUBLISH/SUBSCRIBE/PSUBSCRIBE behavior cannot be unit
// tested with mocks — the pure-logic tests (ParseKind, nil-client guards, interface
// mock-satisfaction) live in go/tests/unit/clients_redis_messaging_test.go; this
// suite targets correctness over a real connection.
package integration

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	msgredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	coreiface "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// RedisMessagingSuite runs the Redis Pub/Sub round-trips against a real container.
type RedisMessagingSuite struct {
	testsuite.RedisIntegrationSuite
}

// TestRedisMessagingSuite is the suite entrypoint.
//
// Why this test is important:
//   - Without a top-level TestXxx function, `go test` ignores every suite method.
//
// What it tests:
//   - Wires RedisMessagingSuite into the testify runner.
func TestRedisMessagingSuite(t *testing.T) {
	suite.Run(t, new(RedisMessagingSuite))
}

// newClient builds a go-redis client against the suite's container (RESP3, no
// Protocol:2 — this is a real Redis), cleaned up when the test ends.
func (s *RedisMessagingSuite) newClient() *goredis.Client {
	c := goredis.NewClient(&goredis.Options{Addr: s.RedisAddr})
	s.T().Cleanup(func() { _ = c.Close() })
	return c
}

// waitSubscribed blocks until Redis reports channel has a subscriber, so a
// publish lands only after the SUBSCRIBE is registered (Pub/Sub drops a message with
// no live subscriber). Condition-based, not a fixed sleep.
func (s *RedisMessagingSuite) waitSubscribed(
	probe *goredis.Client,
	channel string,
) {
	s.Require().Eventually(func() bool {
		res, err := probe.PubSubNumSub(context.Background(), channel).Result()
		return err == nil && res[channel] >= 1
	}, 5*time.Second, 10*time.Millisecond, "subscription for %q not active", channel)
}

// waitUnsubscribed blocks until Redis reports channel has 0 subscribers.
func (s *RedisMessagingSuite) waitUnsubscribed(probe *goredis.Client, channel string) {
	s.Require().Eventually(func() bool {
		res, err := probe.PubSubNumSub(context.Background(), channel).Result()
		return err == nil && res[channel] == 0
	}, 5*time.Second, 10*time.Millisecond, "subscription for %q still active", channel)
}

// waitPatterns blocks until Redis reports at least n active pattern subscriptions.
func (s *RedisMessagingSuite) waitPatterns(probe *goredis.Client, n int64) {
	s.Require().Eventually(func() bool {
		got, err := probe.PubSubNumPat(context.Background()).Result()
		return err == nil && got >= n
	}, 5*time.Second, 10*time.Millisecond, "pattern subscription not active")
}

// chanHandler forwards each message's payload onto out, so a test can observe async
// delivery through the subscriber's dispatch loop with a channel + timeout.
func chanHandler(out chan<- string) coreiface.MessageHandler {
	return func(_ context.Context, msg *coreiface.Message) error {
		out <- string(msg.Payload)
		return nil
	}
}

// recvChan reads one value from ch or fails after a bounded wait (no fixed sleep).
func recvChan[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		require.Failf(t, "timeout", "timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// TestPublisher_PublishesToChannel tests that Publish + PublishBatch + PublishCount
// deliver to a subscribed channel and that PublishCount reports the subscriber count.
//
// Why this test is important:
//   - PUBLISH is the broadcast primitive cross-replica fan-out relies on, and its
//     subscriber count is the signal a caller uses to decide cluster-wide delivery.
//
// What it tests:
//   - Publish delivers one message; PublishBatch delivers each payload; PublishCount
//     delivers AND returns the number of subscribers (1 on a subscribed channel, 0 on
//     a channel with none).
func (s *RedisMessagingSuite) TestPublisher_PublishesToChannel() {
	pub, err := msgredis.NewPublisher(msgredis.Config{Client: s.newClient()})
	s.Require().NoError(err)
	sub, err := msgredis.NewSubscriber(msgredis.Config{Client: s.newClient()})
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = sub.Close() })
	probe := s.newClient()

	ctx := context.Background()
	got := make(chan string, 8)
	s.Require().NoError(sub.Subscribe(ctx, "events", chanHandler(got)))
	s.waitSubscribed(probe, "events")

	// An empty batch is a no-op (short-circuit before any pipeline round-trip).
	s.Require().NoError(pub.PublishBatch(ctx, "events", nil))

	s.Require().NoError(pub.Publish(ctx, "events", []byte("hello")))
	s.Equal("hello", recvChan(s.T(), got, "published message"))

	s.Require().
		NoError(pub.PublishBatch(ctx, "events", [][]byte{[]byte("a"), []byte("b")}))
	batch := []string{recvChan(s.T(), got, "batch 1"), recvChan(s.T(), got, "batch 2")}
	s.ElementsMatch([]string{"a", "b"}, batch)

	// PublishCount reports the subscriber count (the backplane's cluster-delivery signal)
	// and still delivers to that subscriber.
	n, err := pub.PublishCount(ctx, "events", []byte("count-me"))
	s.Require().NoError(err)
	s.Equal(1, n, "one subscriber is listening on events")
	s.Equal("count-me", recvChan(s.T(), got, "counted publish still delivers"))

	// A channel with no subscriber reports 0 (the message is dropped, at-most-once).
	n, err = pub.PublishCount(ctx, "events.nobody", []byte("dropped"))
	s.Require().NoError(err)
	s.Zero(n, "no subscriber on events.nobody")
}

// TestPublisher_Error_Unavailable tests that a publish against an unreachable Redis
// surfaces a coded UNAVAILABLE error.
//
// Why this test is important:
//   - A caller must be able to classify a transient broker outage; a code-less error
//     breaks that classification (ARCHITECTURE.md#error-codes).
//
// What it tests:
//   - A publisher over a client pointed at a dead address returns CodeUnavailable.
func (s *RedisMessagingSuite) TestPublisher_Error_Unavailable() {
	dead := goredis.NewClient(
		&goredis.Options{Addr: "127.0.0.1:1", DialTimeout: time.Second},
	)
	s.T().Cleanup(func() { _ = dead.Close() })
	pub, err := msgredis.NewPublisher(msgredis.Config{Client: dead})
	s.Require().NoError(err)

	err = pub.Publish(context.Background(), "events", []byte("x"))
	s.Require().Error(err)
	s.Equal(apperr.CodeUnavailable, apperr.Code(err))
}

// TestSubscriber_DynamicSubscribeUnsubscribe tests runtime add/remove of channels on
// one multiplexed subscription.
//
// Why this test is important:
//   - A fan-out caller registers/unregisters per-recipient channels as clients come and
//     go over a single shared subscription; a leaked or missing channel misroutes
//     broadcasts.
//
// What it tests:
//   - After Subscribe("A") messages on A arrive; after Unsubscribe("A") + Subscribe("B"),
//     messages on B arrive and a later message on A does not.
func (s *RedisMessagingSuite) TestSubscriber_DynamicSubscribeUnsubscribe() {
	client := s.newClient()
	probe := s.newClient()
	sub, err := msgredis.NewSubscriber(msgredis.Config{Client: client})
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = sub.Close() })

	ctx := context.Background()
	chA := make(chan string, 4)
	chB := make(chan string, 4)

	s.Require().NoError(sub.Subscribe(ctx, "chan.A", chanHandler(chA)))
	s.waitSubscribed(probe, "chan.A")
	s.Require().NoError(probe.Publish(ctx, "chan.A", "a1").Err())
	s.Equal("a1", recvChan(s.T(), chA, "chan.A live"))

	s.Require().NoError(sub.Unsubscribe(ctx, "chan.A"))
	s.waitUnsubscribed(probe, "chan.A")
	s.Require().NoError(sub.Subscribe(ctx, "chan.B", chanHandler(chB)))
	s.waitSubscribed(probe, "chan.B")

	s.Require().NoError(probe.Publish(ctx, "chan.A", "a2").Err())
	s.Require().NoError(probe.Publish(ctx, "chan.B", "b1").Err())
	s.Equal("b1", recvChan(s.T(), chB, "chan.B live"))
	select {
	case m := <-chA:
		s.Failf("unexpected delivery", "on unsubscribed channel A: %q", m)
	default:
	}
}

// TestSubscriber_PatternSubscribeReceivesMatching tests PSUBSCRIBE delivery + dynamic
// PUnsubscribe.
//
// Why this test is important:
//   - Pattern subscriptions let one subscription match a family of channels; a missed
//     match or a leaked pattern misbehaves.
//
// What it tests:
//   - PSubscribe("news.*") receives a publish to "news.sports"; PUnsubscribe stops it.
func (s *RedisMessagingSuite) TestSubscriber_PatternSubscribeReceivesMatching() {
	client := s.newClient()
	probe := s.newClient()
	sub, err := msgredis.NewSubscriber(msgredis.Config{Client: client})
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = sub.Close() })

	ctx := context.Background()
	chP := make(chan string, 4)

	s.Require().NoError(sub.PSubscribe(ctx, "news.*", chanHandler(chP)))
	s.waitPatterns(probe, 1)
	s.Require().NoError(probe.Publish(ctx, "news.sports", "goal").Err())
	s.Equal("goal", recvChan(s.T(), chP, "pattern match"))

	s.Require().NoError(sub.PUnsubscribe(ctx, "news.*"))
	s.Require().Eventually(func() bool {
		got, err := probe.PubSubNumPat(ctx).Result()
		return err == nil && got == 0
	}, 5*time.Second, 10*time.Millisecond, "pattern still active")
	s.Require().NoError(probe.Publish(ctx, "news.weather", "rain").Err())
	select {
	case m := <-chP:
		s.Failf("unexpected delivery", "after PUnsubscribe: %q", m)
	default:
	}
}

// TestSubscriber_FirstCallerCancel_KeepsDelivering tests that cancelling the context
// of the Subscribe call that started the dispatch loop does not stop delivery.
//
// Why this test is important:
//   - The dispatch loop is shared by every subscription and outlives the call that
//     started it; tying it to that call's cancellation would silently stop delivery
//     to every channel once the first request finished.
//
// What it tests:
//   - After the first Subscribe's ctx is cancelled, a message on that channel and on
//     a later-subscribed channel both arrive, and the handler's ctx is not done.
func (s *RedisMessagingSuite) TestSubscriber_FirstCallerCancel_KeepsDelivering() {
	probe := s.newClient()
	sub, err := msgredis.NewSubscriber(msgredis.Config{Client: s.newClient()})
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = sub.Close() })

	firstCtx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 4)
	alive := make(chan bool, 4)
	handler := func(hctx context.Context, msg *coreiface.Message) error {
		alive <- hctx.Err() == nil
		got <- string(msg.Payload)
		return nil
	}
	s.Require().NoError(sub.Subscribe(firstCtx, "first", handler))
	s.waitSubscribed(probe, "first")
	cancel()

	ctx := context.Background()
	s.Require().NoError(sub.Subscribe(ctx, "second", handler))
	s.waitSubscribed(probe, "second")

	s.Require().NoError(probe.Publish(ctx, "first", "f1").Err())
	s.Equal("f1", recvChan(s.T(), got, "first channel after cancel"))
	s.True(recvChan(s.T(), alive, "handler ctx state"), "handler ctx is live")
	s.Require().NoError(probe.Publish(ctx, "second", "s1").Err())
	s.Equal("s1", recvChan(s.T(), got, "second channel after cancel"))
	s.True(recvChan(s.T(), alive, "handler ctx state"), "handler ctx is live")
}

// TestSubscriber_HandlerError_LoggedNotRequeued tests that a handler error is logged
// and the loop keeps delivering — Pub/Sub has no requeue.
//
// Why this test is important:
//   - Redis Pub/Sub cannot nack/redeliver, so a failing handler must not stall the
//     loop or swallow the failure silently.
//
// What it tests:
//   - Two messages whose handler errors are each delivered once (no redelivery) and
//     the second still arrives; the failures are logged.
func (s *RedisMessagingSuite) TestSubscriber_HandlerError_LoggedNotRequeued() {
	client := s.newClient()
	probe := s.newClient()
	spy := fixtures.NewSpyLogger()
	sub, err := msgredis.NewSubscriber(msgredis.Config{Client: client, Logger: spy})
	s.Require().NoError(err)

	ctx := context.Background()
	received := make(chan string, 8)
	failing := func(_ context.Context, msg *coreiface.Message) error {
		received <- string(msg.Payload)
		return apperr.New(apperr.CodeInternal, "boom")
	}
	s.Require().NoError(sub.Subscribe(ctx, "err.chan", failing))
	s.waitSubscribed(probe, "err.chan")

	s.Require().NoError(probe.Publish(ctx, "err.chan", "m1").Err())
	s.Equal("m1", recvChan(s.T(), received, "first delivery"))
	s.Require().NoError(probe.Publish(ctx, "err.chan", "m2").Err())
	s.Equal("m2", recvChan(s.T(), received, "loop continues after error"))
	select {
	case m := <-received:
		s.Failf("redelivery", "message redelivered after handler error: %q", m)
	default:
	}

	// Close drains the dispatch goroutine (wg.Wait), so reading the spy is race-free.
	s.Require().NoError(sub.Close())
	s.Contains((*spy.ChildErrorCalls), "message handle failed")
}

// TestSubscriber_Close_IdempotentAndNilSafe tests that Close and the (P)Unsubscribe
// no-ops are safe on an unused or already-closed subscriber.
//
// Why this test is important:
//   - The subscriber is created lazily (no pubsub until the first Subscribe), and a
//     consumer may Close it, double-Close on shutdown, or Unsubscribe a channel it
//     never subscribed; none may panic.
//
// What it tests:
//   - Close before any Subscribe, a second Close, and (P)Unsubscribe of an unknown key
//     all return nil; a Subscribe after Close returns an error rather than panicking.
func (s *RedisMessagingSuite) TestSubscriber_Close_IdempotentAndNilSafe() {
	sub, err := msgredis.NewSubscriber(msgredis.Config{Client: s.newClient()})
	s.Require().NoError(err)

	ctx := context.Background()
	s.Require().
		NoError(sub.Close())
		// close before any subscribe (nil pubsub)
	s.Require().NoError(sub.Close())               // idempotent
	s.Require().NoError(sub.Unsubscribe(ctx, "x")) // never subscribed
	s.Require().NoError(sub.PUnsubscribe(ctx, "p"))
	s.Require().
		Error(sub.Subscribe(ctx, "y", chanHandler(make(chan string, 1))))
	// closed
}

// TestNewFromConfig_ReusesSharedClient tests that the factory builds a working
// publisher + subscriber over the injected client.
//
// Why this test is important:
//   - The client must share the one live go-redis connection; the factory has no
//     address/credential knobs, so it structurally cannot open its own connection.
//
// What it tests:
//   - A provided client yields a publisher + subscriber that round-trip a message.
func (s *RedisMessagingSuite) TestNewFromConfig_ReusesSharedClient() {
	pub, sub, err := msgredis.NewFromConfig(msgredis.Config{Client: s.newClient()})
	s.Require().NoError(err)
	s.Require().NotNil(pub)
	s.Require().NotNil(sub)
	s.T().Cleanup(func() { _ = sub.Close() })

	ctx := context.Background()
	got := make(chan string, 4)
	s.Require().NoError(sub.Subscribe(ctx, "rt.chan", chanHandler(got)))
	s.waitSubscribed(s.newClient(), "rt.chan")
	s.Require().NoError(pub.Publish(ctx, "rt.chan", []byte("via-shared-client")))
	s.Equal("via-shared-client", recvChan(s.T(), got, "roundtrip via shared client"))
}

// TestNewPublisher_KindRedis tests that the parent messaging factory selects the
// Redis backend by Kind and injects the shared client.
//
// Why this test is important:
//   - Redis is a first-class config-selectable messaging backend; a consumer selects
//     KindRedis + WithRedisClient and gets a decorated publisher and a subscriber.
//
// What it tests:
//   - messaging.NewPublisher(KindRedis, WithRedisClient) + NewSubscriber(KindRedis,
//     WithRedisClient) round-trip a message.
func (s *RedisMessagingSuite) TestNewPublisher_KindRedis() {
	client := s.newClient()
	pub, err := messaging.NewPublisher(
		messaging.KindRedis,
		messaging.WithRedisClient(client),
	)
	s.Require().NoError(err)
	s.Require().NotNil(pub)
	subI, err := messaging.NewSubscriber(
		messaging.KindRedis,
		messaging.WithRedisClient(client),
	)
	s.Require().NoError(err)
	sub, ok := subI.(*msgredis.Subscriber)
	s.Require().True(ok)
	s.T().Cleanup(func() { _ = sub.Close() })

	ctx := context.Background()
	got := make(chan string, 4)
	s.Require().NoError(sub.Subscribe(ctx, "kr.chan", chanHandler(got)))
	s.waitSubscribed(s.newClient(), "kr.chan")
	s.Require().NoError(pub.Publish(ctx, "kr.chan", []byte("kind-redis")))
	s.Equal("kind-redis", recvChan(s.T(), got, "KindRedis roundtrip"))
}
