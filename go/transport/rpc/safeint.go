package rpc

import (
	"fmt"
	"math"
)

// Int32OrPanic converts a signed integer to int32 for proto count/index fields whose
// value is invariant-guaranteed to fit int32 (e.g. a per-tenant count, a chunk index). It
// PANICS if the value is out of range — a deliberate loud failure for a "cannot happen"
// invariant, never a silent truncation that would corrupt a displayed count/index and
// mask the upstream bug.
//
// ⚠️ This is NOT interchangeable with [CountToInt32]. Use Int32OrPanic only when the
// value cannot legitimately exceed int32; for counts that CAN be large
// (document/word/page counts), use CountToInt32, which clamps instead of crashing. It is
// generic so both int and int64 callers share it.
func Int32OrPanic[T ~int | ~int32 | ~int64](n T) int32 {
	if int64(n) > math.MaxInt32 || int64(n) < math.MinInt32 {
		panic(fmt.Sprintf("value '%d' out of int32 range", n))
	}
	return int32(n)
}

// CountToInt32 clamps a non-negative count into the int32 range used by proto count
// fields. Unlike [Int32OrPanic] it NEVER panics — it is the policy for counts that can
// legitimately be large (document/word/page/passage counts, unread-item counts), where
// clamping to MaxInt32 degrades a displayed number rather than crashing the request. A
// negative count clamps to 0.
func CountToInt32(n int64) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}
