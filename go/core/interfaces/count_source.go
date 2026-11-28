package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// CountSource counts the observations behind a query, so a caller can enforce a
// privacy floor on a population without reading the observations themselves.
type CountSource interface {
	// Count returns how many observations the filter selects in the named cube; a nil
	// filter selects the whole cube. An unknown cube is CodeInvalidInput; a storage
	// failure is returned coded.
	Count(ctx context.Context, cube string, filter *types.FilterNode) (int64, error)
}
