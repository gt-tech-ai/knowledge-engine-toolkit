package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// NodeStore is an insert-only, content-addressed repository of nodes. A node is stored
// once under its NodeKey (a scope and the hash of its content) and never changes: a
// second put of the same key keeps the first body.
type NodeStore interface {
	// Put stores node under node.Key. It is idempotent: the same key and body is a
	// no-op (Stored false, Conflict false), and the same key with a different body keeps
	// the first body and reports Conflict true. A malformed node is CodeInvalidInput;
	// a storage failure is returned coded.
	Put(ctx context.Context, node types.Node) (types.PutResult, error)

	// Get returns the node stored under key. A key with no node is CodeNotFound.
	Get(ctx context.Context, key types.NodeKey) (types.Node, error)
}

// RefStore holds named pointers to nodes, each moved by compare-and-swap, so concurrent
// writers cannot silently overwrite one another.
type RefStore interface {
	// Ref returns the hash the ref currently names. A ref that does not exist is
	// CodeNotFound.
	Ref(ctx context.Context, key types.RefKey) (types.Hash, error)

	// Move points the ref at move.To when it currently names move.From; a zero From
	// creates the ref when it does not exist. It reports whether the ref moved: losing
	// the race (the ref names something else, or already exists on a create) is
	// false with a nil error. An invalid move is CodeInvalidInput.
	Move(ctx context.Context, move *types.RefMove) (bool, error)
}
