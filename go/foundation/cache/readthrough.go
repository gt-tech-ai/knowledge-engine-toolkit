package cache

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"golang.org/x/sync/singleflight"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// ReadThroughOption tunes one ReadThrough call.
type ReadThroughOption[T any] func(*readThroughOptions[T])

// readThroughOptions is the resolved option set of one ReadThrough call.
type readThroughOptions[T any] struct {
	// cacheable reports whether a loaded value may be stored; nil stores every value.
	cacheable func(T) bool
}

// WithCacheable stores a loaded value only when cacheable returns true.
func WithCacheable[T any](cacheable func(T) bool) ReadThroughOption[T] {
	return func(o *readThroughOptions[T]) { o.cacheable = cacheable }
}

// ReadThrough is the shared read-through (cache-aside) primitive for a value of type T,
// folding the versioned envelope (Encode/Decode) and single-flight (a stampede of
// concurrent misses on one key collapses to ONE load) into one reuse point for every
// SCOPED cache decorator (suggest, count, …). Its shape mirrors the repos-tier caching
// decorator, which predates this primitive and still holds its own copy of the envelope +
// stampede logic; consolidating that decorator onto ReadThrough is a future DRY cleanup.
//
// Flow: a version-matching hit returns the decoded value WITHOUT calling load; otherwise
// a single load (per key, across concurrent callers) runs, its result is encoded and
// stored with ttl, and returned. A load error propagates and nothing is cached (a cache
// Get/Set failure degrades to the live load, never an error).
//
// A nil c skips the cache entirely but still coalesces concurrent loads per key.
// WithCacheable keeps a rejected value out of the cache while still returning it to every
// coalesced caller. The caller that starts a flight sets its load, cache and options;
// callers that join it get that policy, so every caller of one key should pass the same
// cache and options.
//
// A caller whose ctx is already done returns its CodeCanceled or CodeTimeout error at
// once, without reading the cache or starting a load.
//
// The shared load is detached from the caller that started it: it runs on the
// single-flight goroutine, its cache write uses context.WithoutCancel(ctx), and each
// caller waits only until its own ctx ends (returning a CodeCanceled or CodeTimeout
// error) — so one caller leaving neither aborts the load nor fails the others. load takes
// no context, so a load that needs one should close over context.WithoutCancel(ctx) for
// the same guarantee. A panicking load becomes a CodeInternal error rather than crashing
// the process.
//
// T is stored and returned BY VALUE (the envelope round-trips a value), so concurrent
// callers that share the single-flight result never alias a mutable pointer — pass a
// value type (e.g. types.Page[...] or a small count struct), not a pointer.
func ReadThrough[T any](
	ctx context.Context,
	c interfaces.ByteCache,
	sf *singleflight.Group,
	key string,
	ttl time.Duration,
	version int,
	load func() (T, error),
	opts ...ReadThroughOption[T],
) (T, error) {
	var o readThroughOptions[T]
	for _, opt := range opts {
		opt(&o)
	}
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, contextError(err)
	}
	if c != nil {
		if data, hit := c.Get(ctx, key); hit {
			if val, ok, decErr := Decode[T](data, version); decErr == nil && ok {
				return val, nil
			}
		}
	}
	detached := context.WithoutCancel(ctx)
	flight := sf.DoChan(key, func() (any, error) {
		result, loadErr := recoverLoad(load)
		if loadErr != nil {
			return nil, loadErr
		}
		if c != nil && (o.cacheable == nil || o.cacheable(result)) {
			if encoded, encErr := Encode(result, version); encErr == nil {
				c.Set(detached, key, encoded, ttl)
			}
		}
		return result, nil
	})
	select {
	case <-ctx.Done():
		return zero, contextError(ctx.Err())
	case res := <-flight:
		if res.Err != nil {
			return zero, res.Err
		}
		// The flight returns the exact value our load closure produced (a T), so the
		// assertion is total.
		result, _ := res.Val.(T)
		return result, nil
	}
}

// recoverLoad runs load and turns a panic into a CodeInternal error whose "stack" detail
// is the panicking goroutine's stack: the load runs on the single-flight goroutine, where
// an unrecovered panic would crash the process instead of reaching the caller's recovery
// decorator.
func recoverLoad[T any](load func() (T, error)) (result T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = apperr.WithDetails(
				apperr.New(
					apperr.CodeInternal,
					fmt.Sprintf("cache: load panicked: %v", r),
				),
				map[string]string{"stack": string(debug.Stack())},
			)
		}
	}()
	return load()
}

// contextError codes a caller's context error: a deadline is CodeTimeout, anything else
// CodeCanceled.
func contextError(err error) error {
	if apperr.StdIs(err, context.DeadlineExceeded) {
		return apperr.Wrap(
			err,
			apperr.CodeTimeout,
			"cache: read-through wait timed out",
		)
	}
	return apperr.Wrap(err, apperr.CodeCanceled, "cache: read-through wait canceled")
}
