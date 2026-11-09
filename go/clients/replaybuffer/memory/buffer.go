// Package memory provides an in-process ReplayBuffer backend for single-pod deployments
// (dev / replica=1), where a reconnect lands on the same pod so cross-pod sharing is
// unnecessary. It satisfies the same core/interfaces.ReplayBuffer contract as the Redis
// backend, so selecting it is a config change (replay backend = memory), never a logic
// edit.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Buffer is an in-process ReplayBuffer: a mutex-guarded map of key → an ordered, bounded,
// TTL'd list of retained messages.
type Buffer struct {
	// keys maps each buffer key to its retained tail.
	keys map[string]*keyBuffer
	// maxSize is the maximum retained entries per key (oldest evicted past it).
	maxSize int
	// ttl is the per-key retention window, refreshed on each Append.
	ttl time.Duration
	// mu guards keys.
	mu sync.Mutex
}

// keyBuffer is one key's retained tail.
type keyBuffer struct {
	// expiry is when this key's buffer expires; refreshed on Append.
	expiry time.Time
	// lowWater is the last pruned (acked) message id — the gapless-replay boundary.
	lowWater string
	// msgs is the ordered list of retained messages.
	msgs []interfaces.BufferedMessage
}

// New returns an in-process ReplayBuffer bounded to maxSize entries per key with the
// given TTL.
func New(maxSize int, ttl time.Duration) *Buffer {
	return &Buffer{
		keys:    make(map[string]*keyBuffer),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// compile-time assertion that *Buffer satisfies the seam.
var _ interfaces.ReplayBuffer = (*Buffer)(nil)

// Append records payload under key, evicting the oldest past maxSize and refreshing the
// TTL.
func (b *Buffer) Append(_ context.Context, key, msgID string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	currentKeyBuffer := b.live(key)
	if currentKeyBuffer == nil {
		currentKeyBuffer = &keyBuffer{}
		b.keys[key] = currentKeyBuffer
	}

	currentKeyBuffer.msgs = append(
		currentKeyBuffer.msgs,
		interfaces.BufferedMessage{ID: msgID, Payload: payload},
	)
	if len(currentKeyBuffer.msgs) > b.maxSize {
		// Evict the oldest to stay bounded. Eviction does NOT advance lowWater (that is the
		// ack/prune boundary only): a client whose last-received id was evicted is a gap →
		// full-refresh. This keeps the Redis backend to one pipelined round-trip per Append.
		currentKeyBuffer.msgs = currentKeyBuffer.msgs[len(currentKeyBuffer.msgs)-b.maxSize:]
	}

	currentKeyBuffer.expiry = time.Now().Add(b.ttl)

	return nil
}

// ReplayAfter returns the retained tail strictly after afterMsgID; see the interface doc
// for the complete-flag semantics.
func (b *Buffer) ReplayAfter(
	_ context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	secondKeyBuffer := b.live(key)
	if secondKeyBuffer == nil {
		return nil, false, nil // absent or expired → gap
	}

	// A client that received nothing (unreachable in practice — it always got the
	// ConnectionInfo ack) full-refreshes rather than risk a front gap from eviction.
	if afterMsgID == "" {
		return nil, false, nil
	}

	// afterMsgID == the pruned low-water mark: the client acked exactly up to it, so the
	// whole retained tail is a gapless replay.
	if afterMsgID == secondKeyBuffer.lowWater {
		return clone(secondKeyBuffer.msgs), true, nil
	}

	for i, m := range secondKeyBuffer.msgs {
		if m.ID == afterMsgID {
			return clone(secondKeyBuffer.msgs[i+1:]), true, nil
		}
	}

	return nil, false, nil // not retained → gap
}

// Prune drops entries up to and including upToMsgID, recording it as the low-water mark.
func (b *Buffer) Prune(_ context.Context, key, upToMsgID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	secondKeyBuffer := b.live(key)
	if secondKeyBuffer == nil {
		return nil
	}

	for i, m := range secondKeyBuffer.msgs {
		if m.ID == upToMsgID {
			secondKeyBuffer.lowWater = upToMsgID
			secondKeyBuffer.msgs = clone(secondKeyBuffer.msgs[i+1:])

			return nil
		}
	}

	return nil // not present → no-op
}

// Delete removes the key's buffer entirely.
func (b *Buffer) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.keys, key)

	return nil
}

// live returns key's buffer if it exists and has not expired, else nil (lazily deleting
// an expired one). Callers hold b.mu.
func (b *Buffer) live(key string) *keyBuffer {
	currentKeyBuffer, ok := b.keys[key]
	if !ok {
		return nil
	}

	if time.Now().After(currentKeyBuffer.expiry) {
		delete(b.keys, key)

		return nil
	}

	return currentKeyBuffer
}

// clone copies a message slice so callers never alias the buffer's backing array.
func clone(msgs []interfaces.BufferedMessage) []interfaces.BufferedMessage {
	if len(msgs) == 0 {
		return nil
	}

	out := make([]interfaces.BufferedMessage, len(msgs))
	copy(out, msgs)

	return out
}
