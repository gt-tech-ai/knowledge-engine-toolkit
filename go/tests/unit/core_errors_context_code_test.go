package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TestContextCode_MapsContextErrors tests the context-aware code extractor.
//
// Why this test is important:
//   - A cancelled or timed-out wait is returned by many decorators; coding it as
//     TIMEOUT or CANCELED keeps the retry policy (IsTransient) and the transport
//     status right instead of collapsing both into one generic code
//
// What it tests:
//   - An uncoded context.DeadlineExceeded (also inside a chain) yields TIMEOUT
//   - An uncoded context.Canceled yields CANCELED
//   - An already-coded error keeps its code; any other error yields the fallback
func TestContextCode_MapsContextErrors(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		apperr.CodeTimeout,
		apperr.ContextCode(context.DeadlineExceeded, apperr.CodeInternal),
	)
	assert.Equal(
		t,
		apperr.CodeTimeout,
		apperr.ContextCode(
			apperr.Join(apperr.Sentinel("op"), context.DeadlineExceeded),
			apperr.CodeInternal,
		),
	)
	assert.Equal(
		t,
		apperr.CodeCanceled,
		apperr.ContextCode(context.Canceled, apperr.CodeInternal),
	)
	assert.Equal(
		t,
		apperr.CodeConflict,
		apperr.ContextCode(
			apperr.Wrap(context.Canceled, apperr.CodeConflict, "dup"),
			apperr.CodeInternal,
		),
	)
	assert.Equal(
		t,
		apperr.CodeUnavailable,
		apperr.ContextCode(apperr.Sentinel("sdk"), apperr.CodeUnavailable),
	)
}
