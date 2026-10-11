package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/validator"
)

// stubValidatable is a test DTO whose Validate() returns a preset error — the input to
// the Ensure chokepoint under test (not an external dependency).
type stubValidatable struct{ err error }

func (s stubValidatable) Validate() error { return s.err }

// stubPtrValidatable has a POINTER receiver whose Validate() dereferences the receiver,
// so a typed nil (*stubPtrValidatable)(nil) would panic unless Ensure skips it — the
// guard this test exercises.
type stubPtrValidatable struct{ err error }

func (s *stubPtrValidatable) Validate() error { return s.err }

// TestEnsure_ShortCircuitsOnFirstViolation tests the hand-written-service DTO chokepoint
// guard.
//
// Why this test is important:
//   - Ensure is the single entry point that enforces DTO self-validation below the
//     transport edge
//
// if it did not short-circuit on a coded violation — or did not skip nil optional
//
//	DTOs — invalid data would reach the write path (or a valid call would panic).
//
// What it tests:
//   - Ensure returns the first violation, skips nil entries, and returns nil when every
//     DTO is valid; the returned error carries CodeInvalidInput so it classifies to 400 /
//     InvalidArgument.
func TestEnsure_ShortCircuitsOnFirstViolation(t *testing.T) {
	require.NoError(t, validator.Ensure())                  // no DTOs → nil
	require.NoError(t, validator.Ensure(stubValidatable{})) // one valid DTO
	require.NoError(
		t,
		validator.Ensure(nil, stubValidatable{}),
	) // untyped nil interface entry skipped
	require.NotPanics(
		t,
		func() { // typed nil pointer skipped, not deref'd
			require.NoError(
				t,
				validator.Ensure((*stubPtrValidatable)(nil), stubValidatable{}),
			)
		},
	)

	first := apperr.InvalidInput("first bad dto")
	second := apperr.InvalidInput("second bad dto")
	got := validator.Ensure(
		nil,
		stubValidatable{},
		stubValidatable{err: first},
		stubValidatable{err: second},
	)
	require.ErrorIs(
		t,
		got,
		first,
	) // the FIRST violation, not the second
	require.True(
		t,
		apperr.Is(got, apperr.CodeInvalidInput),
	) // classified as invalid input
}
