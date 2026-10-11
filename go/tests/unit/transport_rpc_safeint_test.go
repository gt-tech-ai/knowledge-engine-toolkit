package unit_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	apprpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rpc"
)

// TestInt32OrPanic tests the invariant-assertion int→int32 policy.
//
// Why this test is important:
//   - It guards values that "cannot" exceed int32 (per-tenant counts, chunk indices); the
//     contract is a LOUD panic on violation, never a silent truncation — a silent wrap
//     would corrupt a displayed count or index and mask the upstream bug. It must accept
//     both int and int64 callers.
//
// What it tests:
//   - An in-range value passes through unchanged (int and int64 callers).
//   - An out-of-range value panics (does not clamp).
func TestInt32OrPanic(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int32(42), apprpc.Int32OrPanic(42))
	assert.Equal(t, int32(42), apprpc.Int32OrPanic(int64(42)))
	assert.Panics(t, func() { apprpc.Int32OrPanic(int64(math.MaxInt32) + 1) })
}

// TestCountToInt32 tests the clamping count→int32 policy.
//
// Why this test is important:
//   - It is the DISTINCT sibling of Int32OrPanic for counts that can legitimately be
//     large (document / word / page counts): it must clamp — never panic — so a huge
//     count degrades a displayed number rather than crashing the request. Unifying the
//     two policies would break one class or the other.
//
// What it tests:
//   - A negative count clamps to 0; an over-MaxInt32 count clamps to MaxInt32; an
//     in-range count passes.
func TestCountToInt32(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int32(0), apprpc.CountToInt32(-5))
	assert.Equal(t, int32(math.MaxInt32), apprpc.CountToInt32(int64(math.MaxInt32)+100))
	assert.Equal(t, int32(1234), apprpc.CountToInt32(1234))
}
