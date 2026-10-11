package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *loggingDecorator satisfies the seam.
var _ interfaces.ReplayBuffer = (*loggingDecorator)(nil)

// loggingDecorator logs each buffer operation at Debug with its outcome and any failure
// at Error, binding the request context so entries carry the active trace/span ids.
// Innermost of the trio.
type loggingDecorator struct {
	// inner is the next buffer in the decorator chain.
	inner interfaces.ReplayBuffer
	// logger writes the structured entries.
	logger interfaces.Logger
	// name labels the buffer instance in every entry.
	name string
}

// Append logs the append outcome or failure, trace-correlated via ctx.
func (d *loggingDecorator) Append(
	ctx context.Context,
	key, msgID string,
	payload []byte,
) error {
	err := d.inner.Append(ctx, key, msgID, payload)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error(
			"replaybuffer append failed",
			"buffer",
			d.name,
			"key",
			key,
			"msg_id",
			msgID,
			"error",
			err,
		)
		return err
	}
	log.Debug("replaybuffer append", "buffer", d.name, "key", key, "msg_id", msgID)
	return nil
}

// ReplayAfter logs the replay outcome (count + complete) or failure, trace-correlated via
// ctx.
func (d *loggingDecorator) ReplayAfter(
	ctx context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	msgs, complete, err := d.inner.ReplayAfter(ctx, key, afterMsgID)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error(
			"replaybuffer replay failed",
			"buffer",
			d.name,
			"key",
			key,
			"after",
			afterMsgID,
			"error",
			err,
		)
		return msgs, complete, err
	}
	log.Debug(
		"replaybuffer replay",
		"buffer",
		d.name,
		"key",
		key,
		"after",
		afterMsgID,
		"count",
		len(msgs),
		"complete",
		complete,
	)
	return msgs, complete, nil
}

// Prune logs the prune outcome or failure, trace-correlated via ctx.
func (d *loggingDecorator) Prune(ctx context.Context, key, upToMsgID string) error {
	err := d.inner.Prune(ctx, key, upToMsgID)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error(
			"replaybuffer prune failed",
			"buffer",
			d.name,
			"key",
			key,
			"up_to",
			upToMsgID,
			"error",
			err,
		)
		return err
	}
	log.Debug("replaybuffer prune", "buffer", d.name, "key", key, "up_to", upToMsgID)
	return nil
}

// Delete logs the delete outcome or failure, trace-correlated via ctx.
func (d *loggingDecorator) Delete(ctx context.Context, key string) error {
	err := d.inner.Delete(ctx, key)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error(
			"replaybuffer delete failed",
			"buffer",
			d.name,
			"key",
			key,
			"error",
			err,
		)
		return err
	}
	log.Debug("replaybuffer delete", "buffer", d.name, "key", key)
	return nil
}
