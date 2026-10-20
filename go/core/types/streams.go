package types

import (
	"io"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Streams are the writers a command reports to: results on Out, progress and
// diagnostics on Err.
type Streams struct {
	// Out receives results (a JSON report, a table).
	Out io.Writer

	// Err receives progress and diagnostics.
	Err io.Writer
}

// Require returns CodeInvalidInput when either writer is missing.
func (s Streams) Require() error {
	if s.Out == nil || s.Err == nil {
		return apperr.New(
			apperr.CodeInvalidInput,
			"streams: out and err are both required",
		)
	}
	return nil
}
