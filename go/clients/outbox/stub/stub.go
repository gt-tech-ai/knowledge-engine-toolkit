// Package stub is the zero-infrastructure OutboxSink: it accepts every record
// and delivers nothing, so the graph boots with no queue or bucket.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Sink accepts and discards every record.
type Sink struct{}

// New returns the stub sink.
func New() *Sink { return &Sink{} }

// Send reports success for every record.
func (*Sink) Send(_ context.Context, recs []types.OutboxRecord) []error {
	return make([]error, len(recs))
}
