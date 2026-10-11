package stream

import (
	"context"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Chunk splits items, in order, into consecutive groups whose sizes sum to at most
// maxBytes; size reports one item's encoded size. The split is greedy: a group closes
// only when the next item would not fit. An item larger than maxBytes on its own is
// CodeResourceExhausted, and a non-positive maxBytes is CodeInvalidInput. No items
// yield no groups.
func Chunk[T any](items []T, size func(T) int, maxBytes int) ([][]T, error) {
	if maxBytes <= 0 {
		return nil, apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("stream: chunk max bytes must be positive, got %d", maxBytes))
	}
	var chunks [][]T
	var cur []T
	used := 0
	for i, item := range items {
		n := size(item)
		if n > maxBytes {
			return nil, apperr.New(apperr.CodeResourceExhausted,
				fmt.Sprintf("stream: item %d is %d bytes, over the %d-byte chunk limit",
					i, n, maxBytes))
		}
		if used+n > maxBytes {
			chunks = append(chunks, cur)
			cur, used = nil, 0
		}
		cur = append(cur, item)
		used += n
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks, nil
}

// Collect drains s into a slice and closes it. Once the items' sizes sum past
// maxBytes it stops reading and returns CodeResourceExhausted; a source error is
// returned unchanged. s is closed on every path.
func Collect[T any](
	ctx context.Context,
	s interfaces.Stream[T],
	size func(T) int,
	maxBytes int,
) (items []T, err error) {
	defer func() {
		if cerr := s.Close(); err == nil && cerr != nil {
			items, err = nil, cerr
		}
	}()
	used := 0
	for {
		item, ok, err := s.Next(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return items, nil
		}
		used += size(item)
		if used > maxBytes {
			return nil, apperr.New(apperr.CodeResourceExhausted,
				fmt.Sprintf("stream: result is over the %d-byte limit", maxBytes))
		}
		items = append(items, item)
	}
}
