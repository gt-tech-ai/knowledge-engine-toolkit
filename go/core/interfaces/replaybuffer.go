package interfaces

import "context"

// BufferedMessage is one retained outbound frame in a ReplayBuffer: its per-connection
// monotonic message id and the opaque marshaled payload to resend verbatim on reconnect.
type BufferedMessage struct {
	// ID is the message's per-connection monotonic id (e.g. "msg-7").
	ID string
	// Payload is the marshaled frame to resend unchanged (encoding-independent bytes).
	Payload []byte
}

// ReplayBuffer buffers a keyed, ordered tail of recent outbound messages so a client
// whose socket briefly drops can be resent exactly the frames it missed (reconnect
// replay). It is the seam the WebSocket hub depends on; callers depend on this interface,
// never a concrete backend. Backends: clients/replaybuffer/memory (in-process,
// dev/single-replica — a reconnect lands on the same pod) and clients/replaybuffer/redis
// (cross-pod, staging/prod), selected by config via clients/replaybuffer.NewFromConfig
// (mirroring clients/lock).
//
// Ordering is INSERTION order, never a comparison of the opaque ids ("msg-10" sorts
// before "msg-9" lexically) — ReplayAfter/Prune locate an id by its position in the
// retained list, so callers must never assume the ids are numerically comparable.
type ReplayBuffer interface {
	// Append records payload (identified by msgID) as the newest entry under key,
	// evicting the oldest beyond the configured bound and refreshing the key's TTL.
	Append(ctx context.Context, key, msgID string, payload []byte) error

	// ReplayAfter returns the retained messages strictly after afterMsgID, in insertion
	// order. complete is true only when afterMsgID is a proven-gapless anchor — either
	// present in the retained window (a gapless tail follows, possibly empty when the
	// caller is already caught up) or exactly the low-water mark set by the last Prune.
	// complete is false for anything else — an evicted or unknown id, a TTL-expired or
	// absent key, or an empty afterMsgID (a client that received nothing; unreachable in
	// practice) — so the caller must full-refresh, never replay a possibly-gapped tail.
	// complete=false is always the safe answer.
	ReplayAfter(
		ctx context.Context,
		key, afterMsgID string,
	) (msgs []BufferedMessage, complete bool, err error)

	// Prune drops every retained entry up to and including upToMsgID — a confirmed prefix
	// the client has acked — leaving the tail, and records upToMsgID as the new low-water
	// mark. An upToMsgID not in the retained window is a no-op.
	Prune(ctx context.Context, key, upToMsgID string) error

	// Delete removes the key's buffer entirely (e.g. after a successful replay).
	Delete(ctx context.Context, key string) error
}
