package stream

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// sliceStream yields the items of a slice in order.
type sliceStream[T any] struct {
	// items is the remaining, not yet yielded items; nil after Close.
	items []T
}

// FromSlice returns a Stream that yields items in order and then ends. It does no I/O
// and never fails.
func FromSlice[T any](items []T) interfaces.Stream[T] {
	return &sliceStream[T]{items: items}
}

// Next implements interfaces.Stream.
func (s *sliceStream[T]) Next(context.Context) (item T, ok bool, err error) {
	var zero T
	if len(s.items) == 0 {
		return zero, false, nil
	}
	item = s.items[0]
	s.items = s.items[1:]
	return item, true, nil
}

// Close implements interfaces.Stream.
func (s *sliceStream[T]) Close() error {
	s.items = nil
	return nil
}
