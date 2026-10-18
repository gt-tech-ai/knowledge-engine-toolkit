package replaybuffer

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every operation of base through the client tier as the client named
// name, one operation per method of its contract.
func Decorate(
	base interfaces.ReplayBuffer,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.ReplayBuffer, error) {
	b, err := client.NewBase[interfaces.ReplayBuffer](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorated{base: base, Base: b}, nil
}

// decorated runs each ReplayBuffer operation through the client tier.
type decorated struct {
	// base is the wrapped buffer.
	base interfaces.ReplayBuffer

	// Base runs the operations.
	client.Base
}

var _ interfaces.ReplayBuffer = (*decorated)(nil)

// Append records payload as the operation "append".
func (d *decorated) Append(ctx context.Context, key, msgID string, payload []byte) error {
	return d.Do(ctx, "append", func(ctx context.Context) error {
		return d.base.Append(ctx, key, msgID, payload)
	})
}

// replay is one ReplayAfter result carried through Exec.
type replay struct {
	// msgs is the replayed tail.
	msgs []interfaces.BufferedMessage

	// complete reports whether the tail is gapless.
	complete bool
}

// ReplayAfter returns the tail after afterMsgID as the operation "replay_after".
func (d *decorated) ReplayAfter(
	ctx context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	r, err := d.Exec(ctx, "replay_after", func(ctx context.Context) (replay, error) {
		msgs, complete, err := d.base.ReplayAfter(ctx, key, afterMsgID)
		return replay{msgs: msgs, complete: complete}, err
	})
	return r.msgs, r.complete, err
}

// Prune drops entries up to upToMsgID as the operation "prune".
func (d *decorated) Prune(ctx context.Context, key, upToMsgID string) error {
	return d.Do(ctx, "prune", func(ctx context.Context) error {
		return d.base.Prune(ctx, key, upToMsgID)
	})
}

// Delete removes the key's buffer as the operation "delete".
func (d *decorated) Delete(ctx context.Context, key string) error {
	return d.Do(ctx, "delete", func(ctx context.Context) error {
		return d.base.Delete(ctx, key)
	})
}
