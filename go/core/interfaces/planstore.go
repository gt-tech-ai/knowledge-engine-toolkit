package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// PlanStore keeps a set of saved plans between the job that makes them and the job
// that applies them; every clients/planstore Kind implements it.
type PlanStore interface {
	// Save keeps every plan under set.Dir and writes set.Manifest there, the hand-off
	// the applying job loads from.
	Save(ctx context.Context, set types.PlanSet) error

	// Load restores the plans set.Manifest lists into set.Dir, refusing any it cannot
	// verify, so the applying job applies exactly those.
	Load(ctx context.Context, set types.PlanSet) error
}
