package listquery

import "context"

// skipCountKeyType is the private context-key type for the skip-count flag (a distinct
// unexported type so no other package can collide with or read the key). Its zero value,
// skipCountKeyType{}, is the context key carrying the skip-count flag.
type skipCountKeyType struct{}

// WithSkipCount returns a CHILD context that tells Run to SKIP computing total_count for
// the offset arm — leaving Total 0 — even for a Count:true reader. The count-cache
// decorator sets it only for its ONE inner List call when it already holds a cached
// total, so the expensive bounded count is not recomputed and the cached value is
// injected instead.
//
// It MUST be applied to a locally-derived child context passed ONLY into that one inner
// List call — never by reassigning/mutating the caller's own ctx — so the flag can never
// leak into a sibling List and silently zero a real total.
func WithSkipCount(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipCountKeyType{}, true)
}

// CountSkipped reports whether WithSkipCount marked this context — the read side of the
// public WithSkipCount writer, so a consumer (or a test verifying the count-cache
// decorator passes the flag to its inner List) can check it without depending on the
// private context key.
func CountSkipped(ctx context.Context) bool {
	skip, _ := ctx.Value(skipCountKeyType{}).(bool)
	return skip
}
