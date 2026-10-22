package client

import "context"

// Call runs one SDK operation of the common `(ctx, *In, ...func(*Opt)) (*Out, error)`
// shape through b as the operation op, retried when the tier retries.
func Call[In, Out, Opt any](
	ctx context.Context,
	b Base,
	op string,
	fn func(context.Context, *In, ...func(*Opt)) (*Out, error),
	in *In,
	opts ...func(*Opt),
) (*Out, error) {
	return b.Exec(
		ctx,
		op,
		func(c context.Context) (*Out, error) { return fn(c, in, opts...) },
	)
}

// CallOnce runs one SDK operation like Call, without retry, for a call whose replay
// may repeat its effect.
func CallOnce[In, Out, Opt any](
	ctx context.Context,
	b Base,
	op string,
	fn func(context.Context, *In, ...func(*Opt)) (*Out, error),
	in *In,
	opts ...func(*Opt),
) (*Out, error) {
	return b.ExecOnce(ctx, op, func(c context.Context) (*Out, error) {
		return fn(c, in, opts...)
	})
}
