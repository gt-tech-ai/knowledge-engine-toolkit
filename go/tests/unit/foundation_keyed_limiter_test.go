package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/keyed"
)

// TestKeyedLimiter_CapsEachKeyIndependently tests the per-key cap.
//
// Why this test is important:
//   - The limiter exists so one tenant's load cannot take every slot; a shared
//     count, or a release that freed a slot twice, would let one key starve or
//     overrun the others.
//
// What it tests:
//   - A key is refused at its cap while another key still acquires.
//   - Releasing frees exactly one slot, and a second release of the same slot is a no-op.
func TestKeyedLimiter_CapsEachKeyIndependently(t *testing.T) {
	t.Parallel()
	l, err := keyed.New(keyed.Config{MaxPerKey: 2})
	require.NoError(t, err)

	r1, ok := l.TryAcquire("org-a")
	require.True(t, ok)
	_, ok = l.TryAcquire("org-a")
	require.True(t, ok)
	_, ok = l.TryAcquire("org-a")
	require.False(t, ok, "org-a is at its cap")
	_, ok = l.TryAcquire("org-b")
	require.True(t, ok, "org-b has its own slots")

	r1()
	r1()
	_, ok = l.TryAcquire("org-a")
	require.True(t, ok, "the release freed one slot")
	_, ok = l.TryAcquire("org-a")
	require.False(t, ok, "a double release frees only one slot")
}

// TestKeyedLimiter_RejectsANonPositiveCap tests config validation.
//
// Why this test is important:
//   - A zero cap would refuse every request; it is a configuration bug to catch at boot.
//
// What it tests:
//   - MaxPerKey <= 0 is CodeInvalidInput.
func TestKeyedLimiter_RejectsANonPositiveCap(t *testing.T) {
	t.Parallel()
	_, err := keyed.New(keyed.Config{})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
