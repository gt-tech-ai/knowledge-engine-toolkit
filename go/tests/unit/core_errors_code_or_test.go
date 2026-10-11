package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TestCodeOr_KeepsCodeElseFallsBack tests the code-with-fallback extractor.
//
// Why this test is important:
//   - Wrapping a client error must keep a code the client already chose (a
//     NOT_FOUND must not become UNAVAILABLE) while still coding an uncoded SDK
//     error, or the transport maps it to the wrong status
//
// What it tests:
//   - A CONFLICT error, also when wrapped, yields CONFLICT
//   - An uncoded error and a nil error yield the fallback
func TestCodeOr_KeepsCodeElseFallsBack(t *testing.T) {
	t.Parallel()
	conflict := apperr.New(apperr.CodeConflict, "dup")

	assert.Equal(
		t,
		apperr.CodeConflict,
		apperr.CodeOr(conflict, apperr.CodeUnavailable),
	)
	assert.Equal(
		t,
		apperr.CodeConflict,
		apperr.CodeOr(
			apperr.Wrap(conflict, apperr.CodeConflict, "ctx"),
			apperr.CodeUnavailable,
		),
	)
	assert.Equal(
		t,
		apperr.CodeUnavailable,
		apperr.CodeOr(apperr.Sentinel("sdk"), apperr.CodeUnavailable),
	)
	assert.Equal(t, apperr.CodeInternal, apperr.CodeOr(nil, apperr.CodeInternal))
}
