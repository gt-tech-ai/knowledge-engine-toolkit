package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/keyed"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
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

	limiter, err := keyed.New(keyed.Config{MaxPerKey: 2})
	require.NoError(t, err)

	callback, found := limiter.TryAcquire(textOrgA)
	require.True(t, found)

	_, found = limiter.TryAcquire(textOrgA)
	require.True(t, found)

	_, found = limiter.TryAcquire(textOrgA)
	require.False(t, found, "org-a is at its cap")

	_, found = limiter.TryAcquire("org-b")
	require.True(t, found, "org-b has its own slots")

	callback()
	callback()

	_, found = limiter.TryAcquire(textOrgA)
	require.True(t, found, "the release freed one slot")

	_, found = limiter.TryAcquire(textOrgA)
	require.False(t, found, "a double release frees only one slot")
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

	_, err := keyed.New(stdx.Zero[keyed.Config]())
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

const (
	// textOrgA is the literal "org-a".
	textOrgA = "org-a"
)
