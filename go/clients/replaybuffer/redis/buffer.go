// Package redis provides a cross-pod ReplayBuffer backend over Redis, for multi-replica
// deployments (staging/prod) where a reconnect may land on a different pod than the one
// that buffered the tail. It satisfies the same core/interfaces.ReplayBuffer contract as
// the in-process memory backend, so selecting it is a config change (replay backend =
// redis), never a logic edit.
//
// Storage: a per-key Redis list `{prefix}{key}` of `msgID\x00payload` entries (insertion
// order), bounded by LTRIM and expired by a per-key TTL; a companion string
// `{prefix}{key}:lw` holds the low-water mark set by Prune (the client's acked boundary),
// so a ReplayAfter for exactly that id still yields a gapless tail. Ordering is by list
// position, never by comparing the opaque ids.
package redis

import (
	"context"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Config tunes the Redis ReplayBuffer.
type Config struct {
	// KeyPrefix namespaces the buffer's Redis keys (default DefaultKeyPrefix); set it to
	// share one Redis between buffers or to keep an existing key layout.
	KeyPrefix string
	// MaxSize bounds the retained entries per key (LTRIM keeps the newest MaxSize).
	MaxSize int
	// TTL is how long a key's buffer lives after its last Append (EXPIRE).
	TTL time.Duration
}

// Buffer is a Redis-backed ReplayBuffer over a shared go-redis client.
type Buffer struct {
	// client is the shared go-redis connection (reused from the lock/backplane pool).
	client *goredis.Client
	// pruneScript runs pruneLua (EVALSHA, falling back to EVAL).
	pruneScript *goredis.Script
	// cfg holds the bound + TTL.
	cfg Config
}

// DefaultKeyPrefix namespaces the buffer's Redis keys when Config.KeyPrefix is empty.
const DefaultKeyPrefix = "replay:"

// New returns a Redis ReplayBuffer over client with the given bound, TTL, and key prefix
// (an empty KeyPrefix uses DefaultKeyPrefix).
func New(client *goredis.Client, cfg Config) *Buffer {
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = DefaultKeyPrefix
	}
	return &Buffer{client: client, cfg: cfg, pruneScript: goredis.NewScript(pruneLua)}
}

// compile-time assertion that *Buffer satisfies the seam.
var _ interfaces.ReplayBuffer = (*Buffer)(nil)

// nul separates the message id from its payload in a list element; message ids are ASCII
// ("msg-N") and never contain it, so a single split recovers both.
const nul = "\x00"

// listKey returns the Redis key holding key's retained list, under the buffer's prefix.
func (b *Buffer) listKey(key string) string { return b.cfg.KeyPrefix + key }

// lwKey returns the Redis key holding key's low-water mark, under the buffer's prefix.
func (b *Buffer) lwKey(key string) string { return b.cfg.KeyPrefix + key + ":lw" }

// Append RPUSHes the entry, trims to MaxSize, and refreshes the TTL — in one pipelined
// round trip (eviction does not touch the low-water mark, matching the memory backend).
func (b *Buffer) Append(ctx context.Context, key, msgID string, payload []byte) error {
	lk := b.listKey(key)
	pipe := b.client.TxPipeline()
	pipe.RPush(ctx, lk, msgID+nul+string(payload))
	pipe.LTrim(ctx, lk, int64(-b.cfg.MaxSize), -1)
	pipe.Expire(ctx, lk, b.cfg.TTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "replaybuffer append")
	}
	return nil
}

// ReplayAfter returns the retained tail strictly after afterMsgID; see the interface doc
// for the complete-flag semantics.
func (b *Buffer) ReplayAfter(
	ctx context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	// The LRANGE and the low-water GET are separate round trips, i.e. not a single atomic
	// snapshot. This is benign: Append (TxPipeline) and Prune (pruneScript) each mutate
	// the list atomically, so a stale read is always a valid PAST state — a gapless
	// suffix — and the worst outcome is replaying a few already-acked frames (deduped
	// client-side by message_id), never a gap. Only Prune's trim must be atomic (it was
	// not, before pruneScript); this read need not be.
	elems, err := b.client.LRange(ctx, b.listKey(key), 0, -1).Result()
	if err != nil {
		return nil, false, apperr.Wrap(err, apperr.CodeInternal, "replaybuffer lrange")
	}
	lw, err := b.client.Get(ctx, b.lwKey(key)).Result()
	if err != nil && !apperr.StdIs(err, goredis.Nil) {
		return nil, false, apperr.Wrap(err, apperr.CodeInternal, "replaybuffer get lw")
	}
	if len(elems) == 0 && lw == "" {
		return nil, false, nil // absent or expired → gap
	}
	if afterMsgID == "" {
		return nil, false, nil // received nothing → full-refresh
	}
	msgs := decodeAll(elems)
	if afterMsgID == lw {
		return msgs, true, nil // acked exactly the low-water mark → whole tail is gapless
	}
	for i, m := range msgs {
		if m.ID == afterMsgID {
			return msgs[i+1:], true, nil
		}
	}
	return nil, false, nil // not retained → gap
}

// pruneScript atomically drops the confirmed prefix up to and including a message id and
// records it as the low-water mark, in one server-side step. The find-index-then-LTRIM
// MUST be atomic: an Append on the same key (the write pump) evicts from the front
// concurrently with a Prune (the read-loop ack handler), so an index computed by a
// Go-side LRANGE would shift before an index-based LTRIM ran — silently dropping retained
// frames while the low-water mark still reported the tail as gapless. The memory backend
// avoids this under its mutex; this Lua script is the Redis equivalent (Redis runs a
// script with no other command interleaved). It matches an entry by the `msgID\x00`
// prefix so "msg-7" never matches "msg-70", and keeps everything after it (Lua's 1-based
// match index equals the 0-based index of the next element, so LTRIM(i, -1) is the
// gapless tail). New wraps it as the Buffer's pruneScript.
const pruneLua = `
local elems = redis.call('LRANGE', KEYS[1], 0, -1)
local prefix = ARGV[1] .. string.char(0)
for i = 1, #elems do
  if string.sub(elems[i], 1, #prefix) == prefix then
    redis.call('LTRIM', KEYS[1], i, -1)
    redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
    return 1
  end
end
return 0
`

// Prune drops entries up to and including upToMsgID and records it as the low-water mark,
// atomically (see pruneScript). An upToMsgID not in the retained window is a no-op. TTL
// is passed in ms because a sub-second TTL (used in tests) would round to 0s and be
// rejected by EX.
func (b *Buffer) Prune(ctx context.Context, key, upToMsgID string) error {
	if err := b.pruneScript.Run(
		ctx, b.client,
		[]string{b.listKey(key), b.lwKey(key)},
		upToMsgID, b.cfg.TTL.Milliseconds(),
	).Err(); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "replaybuffer prune")
	}
	return nil
}

// Delete removes the key's list and low-water mark.
func (b *Buffer) Delete(ctx context.Context, key string) error {
	if err := b.client.Del(ctx, b.listKey(key), b.lwKey(key)).Err(); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "replaybuffer delete")
	}
	return nil
}

// decodeAll splits every list element into its message.
func decodeAll(elems []string) []interfaces.BufferedMessage {
	msgs := make([]interfaces.BufferedMessage, 0, len(elems))
	for _, e := range elems {
		id, payload := decode(e)
		msgs = append(msgs, interfaces.BufferedMessage{ID: id, Payload: payload})
	}
	return msgs
}

// decode splits a `msgID\x00payload` element.
func decode(e string) (id string, payload []byte) {
	id, rest, found := strings.Cut(e, nul)
	if !found {
		return e, nil
	}
	return id, []byte(rest)
}
