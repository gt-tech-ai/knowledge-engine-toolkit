// Package decorgen is the fixture tools/decorgen generates from: Store has every method
// shape a decorator handles, and decorated_gen.go is its committed output.
package decorgen

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

//go:generate go run github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/decorgen/cmd/decorgen -out decorated_gen.go Store

// Store has each method shape: an embedded operation, an error-only operation with a
// variadic tail, an operation returning several values, and methods that are not
// operations (no context, or no error).
//
// interface-composition exemption — a decorgen fixture spanning every method shape.
type Store interface {
	interfaces.Exister[string]

	// Mark records id with reasons.
	Mark(ctx context.Context, id string, reasons ...string) error

	// Pair returns two values.
	Pair(ctx context.Context) (int, string, error)

	// Name is not an operation (no context).
	Name() string

	// Close is not an operation (no error).
	Close(ctx context.Context)
}
