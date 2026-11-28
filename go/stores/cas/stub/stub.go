// Package stub is the stub kind of the cas stores (stores/cas): an empty store, so a
// graph that holds a content-addressed store boots and runs with no database. Nothing
// is ever kept: Put reports the node stored and discards it, Get and Ref find nothing
// (CodeNotFound), and Move reports that no ref moved.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Nodes is the empty node store.
type Nodes struct{}

// Refs is the empty ref store.
type Refs struct{}

// Compile-time checks: Nodes is a NodeStore and Refs a RefStore.
var (
	_ interfaces.NodeStore = Nodes{}
	_ interfaces.RefStore  = Refs{}
)

// Put discards node and reports it stored.
func (Nodes) Put(context.Context, types.Node) (types.PutResult, error) {
	return types.PutResult{Stored: true, Conflict: false}, nil
}

// Get finds nothing: every key is CodeNotFound.
func (Nodes) Get(context.Context, types.NodeKey) (types.Node, error) {
	return types.Node{}, apperr.New(apperr.CodeNotFound, "cas stub: node not found")
}

// Ref finds nothing: every ref is CodeNotFound.
func (Refs) Ref(context.Context, types.RefKey) (types.Hash, error) {
	return types.Hash{}, apperr.New(apperr.CodeNotFound, "cas stub: ref not found")
}

// Move moves nothing.
func (Refs) Move(context.Context, *types.RefMove) (bool, error) { return false, nil }
