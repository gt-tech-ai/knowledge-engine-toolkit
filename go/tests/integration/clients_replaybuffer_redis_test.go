//go:build integration

// Package integration verifies the Redis ReplayBuffer backend against a real Redis
// instance (redis:7-alpine via the shared RedisIntegrationSuite fixture). These tests
// exercise the real RPUSH/LTRIM/EXPIRE/LRANGE list semantics + the companion
// low-water-mark key that no mock or in-memory substitute can validate (the
// SQL/driver-behaviour analogue for a list store).
package integration

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/suite"

	replayredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// ReplayBufferRedisSuite holds a real Redis container and a go-redis client backing the
// buffer.
type ReplayBufferRedisSuite struct {
	client *goredis.Client
	testsuite.RedisIntegrationSuite
}

// TestReplayBufferRedisSuite runs all Redis ReplayBuffer integration tests.
//
// Why this test is important:
//   - Without a top-level TestXxx, `go test` ignores every suite method.
//
// What it tests:
//   - Suite entrypoint wires ReplayBufferRedisSuite into the testify runner.
func TestReplayBufferRedisSuite(t *testing.T) {
	suite.Run(t, new(ReplayBufferRedisSuite))
}

func (s *ReplayBufferRedisSuite) SetupTest() {
	s.client = goredis.NewClient(&goredis.Options{Addr: s.RedisAddr})
	s.Require().NoError(s.client.FlushDB(context.Background()).Err())
}

func (s *ReplayBufferRedisSuite) TearDownTest() {
	if s.client != nil {
		_ = s.client.Close()
	}
}

func (s *ReplayBufferRedisSuite) newBuffer(
	maxSize int,
	ttl time.Duration,
) interfaces.ReplayBuffer {
	return replayredis.New(s.client, replayredis.Config{MaxSize: maxSize, TTL: ttl})
}

func (s *ReplayBufferRedisSuite) appendSeq(b interfaces.ReplayBuffer, key string, n int) {
	for i := 1; i <= n; i++ {
		id := "msg-" + itoaI(i)
		s.Require().NoError(
			b.Append(context.Background(), key, id, []byte("payload-"+id)),
		)
	}
}

// TestAppendReplayRoundtrip tests that appended messages are replayed as the gapless tail
// after a given id, with payloads preserved byte-for-byte — against real Redis list
// semantics.
//
// Why this test is important:
//   - The whole feature is "resend exactly what the client missed": this proves RPUSH
//     ordering, LRANGE retrieval, the message-id/payload framing (\x00 split), and the
//     after-id slice all work against a real server, not just the in-memory backend.
//
// What it tests:
//   - after appending msg-1..5, ReplayAfter(msg-2) returns msg-3,4,5 in order (complete)
//     with their exact payloads.
func (s *ReplayBufferRedisSuite) TestAppendReplayRoundtrip() {
	b := s.newBuffer(100, time.Minute)
	s.appendSeq(b, "k", 5)

	tail, complete, err := b.ReplayAfter(context.Background(), "k", "msg-2")
	s.Require().NoError(err)
	s.True(complete)
	s.Require().Len(tail, 3)
	s.Equal("msg-3", tail[0].ID)
	s.Equal([]byte("payload-msg-3"), tail[0].Payload)
	s.Equal([]string{"msg-3", "msg-4", "msg-5"}, idsI(tail))
}

// TestNBoundEvicts tests that LTRIM bounds the list to MaxSize and a request for an
// evicted id is a gap.
//
// Why this test is important:
//   - The buffer must not grow without bound in Redis; LTRIM enforces the cap. A client
//     that fell past the window must get complete=false so it full-refreshes rather than
//     replaying a holed tail.
//
// What it tests:
//   - with MaxSize=3, appending msg-1..5 retains msg-3,4,5; ReplayAfter(msg-1) is a gap;
//     ReplayAfter(msg-3) → msg-4,5.
func (s *ReplayBufferRedisSuite) TestNBoundEvicts() {
	b := s.newBuffer(3, time.Minute)
	s.appendSeq(b, "k", 5)

	_, complete, err := b.ReplayAfter(context.Background(), "k", "msg-1")
	s.Require().NoError(err)
	s.False(complete, "evicted id → gap")

	tail, complete, err := b.ReplayAfter(context.Background(), "k", "msg-3")
	s.Require().NoError(err)
	s.True(complete)
	s.Equal([]string{"msg-4", "msg-5"}, idsI(tail))
}

// TestPrune tests that Prune drops the acked prefix and records the low-water mark, so a
// reconnect at the acked id still replays the tail gaplessly.
//
// Why this test is important:
//   - The live AckMessage path prunes confirmed messages to bound the buffer; the
//     low-water-mark key is what lets a reconnect at exactly the acked id still be a
//     complete replay rather than a gap.
//
// What it tests:
//   - after Prune(msg-3), ReplayAfter(msg-3) returns msg-4,5 (complete) and
//     ReplayAfter(msg-1) is a gap.
func (s *ReplayBufferRedisSuite) TestPrune() {
	b := s.newBuffer(100, time.Minute)
	s.appendSeq(b, "k", 5)

	s.Require().NoError(b.Prune(context.Background(), "k", "msg-3"))

	tail, complete, err := b.ReplayAfter(context.Background(), "k", "msg-3")
	s.Require().NoError(err)
	s.True(complete)
	s.Equal([]string{"msg-4", "msg-5"}, idsI(tail))

	_, complete, err = b.ReplayAfter(context.Background(), "k", "msg-1")
	s.Require().NoError(err)
	s.False(complete, "pruned prefix → gap")
}

// TestTTLExpiresAndDelete tests that a key's buffer expires after its TTL (EXPIRE) and
// that Delete removes it immediately — both surfacing as a gap on the next ReplayAfter.
//
// Why this test is important:
//   - The buffer is short-lived recovery state; EXPIRE must actually reclaim it, and
//     Delete (called after a successful replay) must remove both the list and its
//     low-water key so stale state never lingers. Real Redis TTL rounding/DEL behaviour
//     is invisible to an in-memory fake.
//
// What it tests:
//   - with a 1s TTL, ReplayAfter eventually reports a gap; and after Delete, ReplayAfter
//     is a gap.
func (s *ReplayBufferRedisSuite) TestTTLExpiresAndDelete() {
	b := s.newBuffer(100, time.Second)
	s.appendSeq(b, "k", 3)
	s.Eventually(func() bool {
		_, complete, err := b.ReplayAfter(context.Background(), "k", "msg-1")
		return err == nil && !complete
	}, 4*time.Second, 100*time.Millisecond, "buffer must expire after its TTL")

	b2 := s.newBuffer(100, time.Minute)
	s.appendSeq(b2, "d", 3)
	s.Require().NoError(b2.Delete(context.Background(), "d"))
	_, complete, err := b2.ReplayAfter(context.Background(), "d", "msg-1")
	s.Require().NoError(err)
	s.False(complete, "deleted buffer → gap")
}

// TestKeyPrefix tests that the buffer's Redis keys live under its configured prefix —
// "replay:" by default — and that buffers with different prefixes don't see each other's
// entries.
//
// Why this test is important:
//   - The prefix is the buffer's whole footprint in a shared Redis: a consumer keeps its
//     existing key layout across an upgrade by setting it, and two buffers on one Redis
//     must not collide.
//
// What it tests:
//   - A default buffer writes replay:k and replay:k:lw; a buffer with KeyPrefix
//     "app:replay:" writes app:replay:k and app:replay:k:lw; each replays only its own
//     entries for the same key.
func (s *ReplayBufferRedisSuite) TestKeyPrefix() {
	ctx := context.Background()
	def := s.newBuffer(100, time.Minute)
	custom := replayredis.New(s.client, replayredis.Config{
		MaxSize: 100, TTL: time.Minute, KeyPrefix: "app:replay:",
	})
	s.appendSeq(def, "k", 3)
	s.Require().NoError(custom.Append(ctx, "k", "msg-8", []byte("custom-8")))
	s.Require().NoError(custom.Append(ctx, "k", "msg-9", []byte("custom-9")))
	s.Require().NoError(def.Prune(ctx, "k", "msg-1"))
	s.Require().NoError(custom.Prune(ctx, "k", "msg-8"))

	keys, err := s.client.Keys(ctx, "*").Result()
	s.Require().NoError(err)
	s.ElementsMatch(
		[]string{"replay:k", "replay:k:lw", "app:replay:k", "app:replay:k:lw"},
		keys,
	)

	tail, complete, err := def.ReplayAfter(ctx, "k", "msg-1")
	s.Require().NoError(err)
	s.True(complete)
	s.Equal([]string{"msg-2", "msg-3"}, idsI(tail))

	tail, complete, err = custom.ReplayAfter(ctx, "k", "msg-8")
	s.Require().NoError(err)
	s.True(complete)
	s.Equal([]string{"msg-9"}, idsI(tail))
}

func itoaI(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func idsI(msgs []interfaces.BufferedMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}
