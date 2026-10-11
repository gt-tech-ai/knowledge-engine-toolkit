package decorate

import "context"

// Exec runs fn for the named operation under the middleware chain mw and returns fn's
// typed result. The result is carried out of the type-erased middleware core (which
// only sees func(context.Context) error) via a captured variable, so R is arbitrary —
// a value, a pointer, a slice, a *Page, or struct{} for an error-only operation. This
// is what removes the (*T,error) shoehorn of the former ExecWithDecoration seam.
//
// Contract: Exec returns exactly what fn returns, unmodified, on both the success and
// error paths — the result is never zeroed when fn returns an error, so a partial
// result reported alongside an error survives the decoration. The
// chain is synchronous and single-call, so the closure capture is race-free.
func Exec[R any](
	ctx context.Context,
	mw OpMiddleware,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	var result R
	err := mw.WrapOp(ctx, op, func(ctx context.Context) error {
		var e error
		result, e = fn(ctx)
		return e
	})
	return result, err
}
